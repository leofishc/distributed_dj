package main

import (
	"./clientlib"
	"fmt"
	"os"
)

func main() {
	args := os.Args[1:]
	if len(args) < 1 {
		fmt.Println("Need config file name.")
		return
	}
	_, err := clientlib.Initialize(args[0])
	if err != nil {
		fmt.Println("Error initializing client:", err)
	}
	fmt.Println("Client initialized")
}
