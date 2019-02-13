package main

import (
	"./terminal_lib"
	"bufio"
	"fmt"
	"os"
	"strings"
)

func main() {
	args := os.Args[1:]
	if len(args) < 1 {
		fmt.Println("Need config file name.")
		return
	}
	terminal, err := terminal_lib.Initialize(args[0])

	if err != nil {
		fmt.Println("Error initializing client:", err)
	}

	fmt.Println("Terminal initialized with id: ", terminal.GetID())
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("Enter 'show' to show list of songs to vote for or song file name to vote for song!")
	fmt.Println("Enter 'start' to start playing")
	fmt.Println("Enter 'skip' to skip the current song")

	for {
		fmt.Print("-> ")
		text, _ := reader.ReadString('\n')
		// convert CRLF to LF
		text = strings.Replace(text, "\n", "", -1)
		switch text {
		case "show":
			fmt.Println("List of Songs Available to Vote for:")
			songs := terminal.GetAvailableSongs()

			for _, song := range songs {
				fmt.Println(song)
			}
		case "start":
			fmt.Println(terminal.StartMusic())

		case "stop":
			fmt.Println(terminal.StopMusic())
		case "skip":
			fmt.Println(terminal.SkipSong())
		case "info":
			fmt.Println(terminal.GetCurrentSong())
		default:
			if len(text) > 4 {
				if string(text[len(text)-4:]) == ".mp3" {
					err := terminal.CastVote(text)
					if err != nil {
						reason := "of an error"
						if err.Error() == "votenotcast" {
							reason = "2PC aborted or timed out."
						} else if err.Error() == "songnotfound" {
							reason = "the song isn't available."
						}
						fmt.Println("Vote for", text, "could not be cast because", reason)
						fmt.Println(err.Error())
					} else {
						fmt.Println("Vote for", text, "cast")
					}

				}
			} else {
				fmt.Println("Incorrect Usage, Try Again")
			}
		}

	}
}
