package main

import (
	"fmt"
	"time"
)

func greet(name string) string {
	message := fmt.Sprintf("Hello, %s!", name)
	fmt.Println(message)
	return message
}

func main() {
	for index := 0; index < 5; index++ {
		greeting := greet(fmt.Sprintf("debugger %d", index))
		fmt.Println(greeting)
		time.Sleep(time.Second)
	}
}
