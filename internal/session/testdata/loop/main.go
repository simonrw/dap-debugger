// Command loop is a long-running target for attach tests.
package main

import (
	"fmt"
	"time"
)

func tick(count int) string {
	message := fmt.Sprintf("tick %d", count)
	fmt.Println(message)
	return message
}

func main() {
	for count := 0; ; count++ {
		tick(count)
		time.Sleep(100 * time.Millisecond)
	}
}
