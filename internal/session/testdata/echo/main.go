// Command echo reads input through a terminal for terminal tests.
package main

import (
	"bufio"
	"fmt"
	"os"
)

func main() {
	info, _ := os.Stdin.Stat()
	fmt.Print("name? ")
	name, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	fmt.Printf("hello %s", name)
	fmt.Printf("secret has 6 characters, tty: %v\n", info.Mode()&os.ModeCharDevice != 0)
}
