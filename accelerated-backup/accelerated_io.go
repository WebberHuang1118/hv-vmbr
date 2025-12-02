package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// Task represents a read task.
type Task struct {
	index  int
	offset int64
	size   int
}

// Result holds the result of a read task.
type Result struct {
	index int
	data  []byte
	err   error
}

// WriteTask represents a write operation.
type WriteTask struct {
	index  int
	offset int64
	data   []byte
}

// BLKGETSIZE64 is the Linux-specific ioctl request code to get the size of a block device in bytes.
const BLKGETSIZE64 = 0x80081272

// progressTicker controls the progress reporting interval.
const progressTicker = 1 * time.Second

// blockDeviceSize tries to get the total size of a block device using ioctl.
// If it fails (for example because it's not a block device), it returns an error.
func blockDeviceSize(f *os.File) (int64, error) {
	var size int64
	_, _, errNo := syscall.Syscall(
		syscall.SYS_IOCTL,
		f.Fd(),
		BLKGETSIZE64,
		uintptr(unsafe.Pointer(&size)),
	)
	if errNo != 0 {
		return 0, errNo
	}
	return size, nil
}

// readWorker processes read tasks.
func readWorker(file *os.File, tasks <-chan Task, results chan<- Result) {
	for task := range tasks {
		buf := make([]byte, task.size)
		n, err := file.ReadAt(buf, task.offset)
		if err != nil && err != io.EOF {
			results <- Result{index: task.index, err: err}
			continue
		}
		results <- Result{index: task.index, data: buf[:n]}
	}
}

// writeWorker processes write tasks.
func writeWorker(device *os.File, tasks <-chan WriteTask, wg *sync.WaitGroup) {
	defer wg.Done()
	for task := range tasks {
		n, err := device.WriteAt(task.data, task.offset)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error writing block %d at offset %d: %v\n", task.index, task.offset, err)
			os.Exit(1)
		}
		if n != len(task.data) {
			fmt.Fprintf(os.Stderr, "Short write for block %d at offset %d\n", task.index, task.offset)
			os.Exit(1)
		}
	}
}

// runProgressTicker starts a ticker that prints progress updates until the done channel is closed.
// The getCurrent function should return the current byte count,
// totalSize is the device's total size, and label is a string (e.g., "READ" or "WRITE").
func runProgressTicker(done <-chan struct{}, getCurrent func() int64, totalSize int64, label string) {
	ticker := time.NewTicker(progressTicker)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			current := getCurrent()
			percent := float64(current) / float64(totalSize) * 100
			// \r returns to start of line, and \033[K clears the rest of the line.
			fmt.Fprintf(os.Stderr, "%s progress: %d/%d bytes (%.2f%%)\n", label, current, totalSize, percent)
		case <-done:
			// Clear the line and then end.
			fmt.Fprintln(os.Stderr)
			return
		}
	}
}

// readBlockDevice reads from the block device, reorders results, and reports progress.
func readBlockDevice(devicePath string, blockSize, workers int) {
	file, err := os.Open(devicePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening device: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	// Determine total device size.
	totalSize, err := blockDeviceSize(file)
	if err != nil {
		stat, statErr := file.Stat()
		if statErr != nil {
			fmt.Fprintf(os.Stderr, "Error stat'ing device: %v\n", statErr)
			os.Exit(1)
		}
		totalSize = stat.Size()
	}

	tasks := make(chan Task, workers)
	results := make(chan Result, workers)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			readWorker(file, tasks, results)
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	// Generate read tasks.
	go func() {
		var offset int64
		index := 0
		for offset < totalSize {
			size := blockSize
			if offset+int64(size) > totalSize {
				size = int(totalSize - offset)
			}
			tasks <- Task{index: index, offset: offset, size: size}
			offset += int64(size)
			index++
		}
		close(tasks)
	}()

	var bytesRead int64
	done := make(chan struct{})
	go runProgressTicker(done, func() int64 {
		return atomic.LoadInt64(&bytesRead)
	}, totalSize, "READ")

	expected := 0
	buffer := make(map[int][]byte)
	for res := range results {
		if res.err != nil {
			fmt.Fprintf(os.Stderr, "Error in worker (block %d): %v\n", res.index, res.err)
			os.Exit(1)
		}
		if res.index == expected {
			_, _ = os.Stdout.Write(res.data)
			atomic.AddInt64(&bytesRead, int64(len(res.data)))
			expected++
			for {
				if data, ok := buffer[expected]; ok {
					_, _ = os.Stdout.Write(data)
					atomic.AddInt64(&bytesRead, int64(len(data)))
					delete(buffer, expected)
					expected++
				} else {
					break
				}
			}
		} else {
			buffer[res.index] = res.data
		}
	}
	close(done)
}

// writeBlockDevice reads data from stdin and writes it to the block device, reporting progress.
func writeBlockDevice(devicePath string, blockSize, workers int) {
	device, err := os.OpenFile(devicePath, os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening device for writing: %v\n", err)
		os.Exit(1)
	}
	defer device.Close()

	totalSize, err := blockDeviceSize(device)
	if err != nil {
		stat, statErr := device.Stat()
		if statErr != nil {
			fmt.Fprintf(os.Stderr, "Error stat'ing device: %v\n", statErr)
			os.Exit(1)
		}
		totalSize = stat.Size()
	}

	tasks := make(chan WriteTask, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go writeWorker(device, tasks, &wg)
	}

	var bytesWritten int64
	done := make(chan struct{})
	go runProgressTicker(done, func() int64 {
		return atomic.LoadInt64(&bytesWritten)
	}, totalSize, "WRITE")

	index := 0
	offset := int64(0)
	for {
		buf := make([]byte, blockSize)
		n, err := io.ReadFull(os.Stdin, buf)
		if err == io.EOF {
			break
		} else if err == io.ErrUnexpectedEOF {
			tasks <- WriteTask{index: index, offset: offset, data: buf[:n]}
			atomic.AddInt64(&bytesWritten, int64(n))
			break
		} else if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
			os.Exit(1)
		}
		tasks <- WriteTask{index: index, offset: offset, data: buf[:n]}
		atomic.AddInt64(&bytesWritten, int64(n))
		offset += int64(n)
		index++
	}

	close(tasks)
	wg.Wait()
	close(done)
}

func main() {
	var devicePath string
	var blockSize int
	var workers int
	var mode string

	flag.StringVar(&devicePath, "device", "", "Path to block device (e.g., /dev/xvda)")
	flag.IntVar(&blockSize, "bs", 64*1024, "Block size in bytes")
	flag.IntVar(&workers, "workers", 4, "Number of concurrent workers")
	flag.StringVar(&mode, "mode", "", "Mode: 'read' or 'write'")
	flag.Parse()

	if devicePath == "" {
		fmt.Fprintln(os.Stderr, "Error: No device specified. Use -device flag.")
		os.Exit(1)
	}

	if mode == "read" {
		readBlockDevice(devicePath, blockSize, workers)
	} else if mode == "write" {
		writeBlockDevice(devicePath, blockSize, workers)
	} else {
		fmt.Fprintln(os.Stderr, "Error: Invalid mode. Use -mode=read or -mode=write")
		os.Exit(1)
	}
}
