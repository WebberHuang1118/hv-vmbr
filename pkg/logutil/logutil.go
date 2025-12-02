package logutil

import (
	"log"
)

const (
	ColorGreen  = "\033[1;32m"
	ColorYellow = "\033[1;33m"
	ColorRed    = "\033[1;31m"
	ColorBlue   = "\033[1;34m"
	ColorReset  = "\033[0m"
)

func Info(msg string) {
	log.Printf("%s%s%s", ColorGreen, msg, ColorReset)
}

func Warn(msg string) {
	log.Printf("%s%s%s", ColorYellow, msg, ColorReset)
}

func Error(msg string) {
	log.Printf("%s%s%s", ColorRed, msg, ColorReset)
}

func Infof(format string, args ...interface{}) {
	log.Printf(ColorGreen+format+ColorReset, args...)
}

func Warnf(format string, args ...interface{}) {
	log.Printf(ColorYellow+format+ColorReset, args...)
}

func Errorf(format string, args ...interface{}) {
	log.Printf(ColorRed+format+ColorReset, args...)
}

func Fatalf(format string, args ...interface{}) {
	log.Fatalf(ColorRed+format+ColorReset, args...)
}
