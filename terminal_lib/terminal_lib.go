package terminal_lib

import (
	"../util"
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/DistributedClocks/GoVector/govec"
	"github.com/DistributedClocks/GoVector/govec/vrpc"
	"github.com/gordonklaus/portaudio"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type TerminalLib interface {
	GetID() string
	GetAvailableSongs() []string
	StartMusic() string
	StopMusic() string
	CastVote(string) error
	InitStream(string)
	InitializeClient(int) error
	UploadSong() error
	SkipSong() string
	GetCurrentSong() string
}

type terminal_lib struct {
}

var CONFIG Config
var logger *govec.GoLog
var options govec.GoLogOptions
var logging *log.Logger

var currentHostAddr string
var currentHostAddrMutex = sync.Mutex{}

var currentSong string


var stream *portaudio.Stream

var terminalInstance TerminalLib

type Config struct {
	TerminalID   string
	ClientAddr   string
	SongPath     string
	SongAddr     string
	TerminalAddr string
}

// RPC STRUCTS

type VoteInfoReply struct {
	Songs             map[string]util.Song
	CurrentHost       string
	Error             error
	StreamersForSongs map[string][]string
}

type VoteInfoArgs struct {
	TerminalID string
}

type VoteReply struct {
	Error error
}

type VoteArgs struct {
	TerminalID string
	SongName   string
}

type StreamArgs struct {
	HostIPPort string
}

func initConfig(configFile string, config *Config) error {
	configJson, err := os.Open(configFile)
	if err != nil {
		Logln(err)
		return err
	}
	defer configJson.Close()
	byteValue, _ := ioutil.ReadAll(configJson)

	json.Unmarshal(byteValue, config)
	return nil
}

func (terminal *terminal_lib) GetID() string {
	return CONFIG.TerminalID
}

func (terminal *terminal_lib) GetAvailableSongs() []string {
	client, err := vrpc.RPCDial("tcp", CONFIG.ClientAddr, logger, options)
	var songlist []string
	if err != nil {
		
	} else {
		defer client.Close()
		prepareArgs := VoteInfoArgs{CONFIG.ClientAddr}
		var reply VoteInfoReply

		err = client.Call("TerminalServices.GetVotingInfo", prepareArgs, &reply)
		if err != nil {
			
		} else {

			streamersSongs := reply.StreamersForSongs

			for _, song := range reply.Songs {
				streamersString := ""
				for _, streamerIPs := range streamersSongs[song.Hash] {
					streamersString += streamerIPs + " "
				}

				songlist = append(songlist, song.Name+" Hash:"+song.Hash+" streamer(s): "+streamersString)
			}
			return songlist
		}
	}
	return []string{}
}

func (terminal *terminal_lib) CastVote(songName string) error {
	Logln("Casting vote for:", songName)
	client, err := vrpc.RPCDial("tcp", CONFIG.ClientAddr, logger, options)
	if err != nil {
		
		Logln("Could not connect to client to cast vote:", err.Error())
	} else {
		defer client.Close()
		voteArgs := VoteArgs{CONFIG.TerminalID, songName}
		var reply VoteReply

		err = client.Call("TerminalServices.CastVote", voteArgs, &reply)
		if err != nil {
			
			Logln("RPC call to cast vote failed:", err.Error())
		} else if reply.Error != nil {
			
			Logln("Client responded with error from casting vote:", reply.Error.Error())
			return reply.Error
		} else {
			return nil
		}
	}
	return nil
}

func Initialize(file_name string) (TerminalLib, error) {
	terminalInstance = &terminal_lib{}
	path := "./" + file_name

	err := initConfig(path, &CONFIG)
	if err != nil {
		return terminalInstance, err
	}

	//Setup Logging
	nm := CONFIG.TerminalID + "-terminal.log"
	file, logErr := os.OpenFile(nm, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0666)
	if logErr != nil {
		return nil, logErr
	}
	logging = log.New(file, "", log.Lmicroseconds)

	songs, _ := ioutil.ReadDir(CONFIG.SongPath)

	if len(songs) < 1 {
		var e = errors.New("No song in directory, terminating...")
		return nil, e
	}
	Logln("You have", len(songs), "songs to share.")

	logger = govec.InitGoVector(CONFIG.TerminalID, CONFIG.TerminalID, govec.GetDefaultConfig())
	options = govec.GetDefaultLogOptions()

	terminalInstance.InitializeClient(len(songs))
	terminalInstance.UploadSong()

	go detectSwitchStream()

	return terminalInstance, nil
}

func (terminal *terminal_lib) InitializeClient(song_num int) error {
	Logln("Attempting to connect to client:", CONFIG.ClientAddr)
	client, err := vrpc.RPCDial("tcp", CONFIG.ClientAddr, logger, options)

	if err != nil {
		Logln("Failed to connect: ", CONFIG.ClientAddr, err)
		return err
	} else {
		if client != nil {
			defer client.Close()
		}

		init_msg := util.InitMessage{CONFIG.TerminalID, CONFIG.TerminalAddr, song_num}

		logger.LogLocalEvent("Handshake with client: "+CONFIG.ClientAddr, govec.GetDefaultLogOptions())

		var reply util.TerminalInitReply

		err = client.Call("TerminalServices.Init", init_msg, &reply)

		if err != nil {
			Logln("Failed to initialize with client", CONFIG.ClientAddr, err)
			return err
		} else if reply.HostStreamAddr != "" {
			Logln("Initialized with client: ", CONFIG.ClientAddr)
			Logln("Current host's streaming address: ", reply.HostStreamAddr)

			setCurrentHostAddr(reply.HostStreamAddr)
			currentSong = reply.CurrentSongName

			terminal.InitStream(reply.HostStreamAddr)
			return nil
		} else {
			Logln(CONFIG.ClientAddr, "declined the init request.")
			return errors.New("Peer does not have a current host set. Try again when there is a host.")
		}
	}
}

func (terminal *terminal_lib) InitStream(hostAddr string) {
	Logln("Initializing stream with host:", hostAddr)
	const sampleRate = 44100
	numSamples := sampleRate

	streamURL := "http://" + hostAddr
	if getIP(hostAddr) == getIP(CONFIG.ClientAddr) {
		streamURL += "/cacheaudio"
		Logln("Will stream from my client via /cacheaudio.")
	} else {
		streamURL += "/audio"
		Logln("Will stream from another client via /audio.")
	}

	Logln("Stream url:", streamURL)
	portaudio.Initialize()
	Logln("Initialized PortAudio")

	samples := make([]int16, numSamples)

	lastSampleCachePlayed := 0
	dualSamplesCache := make([][]int16, 2)
	for i := range dualSamplesCache {
		dualSamplesCache[i] = make([]int16, numSamples)
	}

	buffering := true

	failures := 0

	newStream, err := portaudio.OpenDefaultStream(0, 1, sampleRate, len(samples), func(out []int16) {
		go func(overwriteIndex int) {
			resp, err := http.Get(streamURL)
			if err == nil {
				defer resp.Body.Close()
				failures = 0
				body, _ := ioutil.ReadAll(resp.Body)
				responseReader := bytes.NewReader(body)
				binary.Read(responseReader, binary.LittleEndian, &samples)

				copy(dualSamplesCache[overwriteIndex], samples)
			} else {
				failures++
				if failures >= 3 {
					Logln("Failures: " + strconv.Itoa(failures))
					portaudio.Terminate()
					Logln("Terminated PortAudio:", err.Error())
					notifyClientOfUnreachableHost(hostAddr)
				}
			}
		}(lastSampleCachePlayed)

		if !buffering && failures == 0 {
			lastSampleCachePlayed = (lastSampleCachePlayed + 1) % 2
			sample := dualSamplesCache[lastSampleCachePlayed]
			for i := range out {
				out[i] = sample[i]
			}
		} else {
			buffering = false
		}
	})

	stream = newStream
	Logln("Chk InitStream")
	chk(err)
}

func notifyClientOfUnreachableHost(hostAddr string) {
	Logln("Notifying client that host is unreachable" + strings.Split(hostAddr, ":")[0])
	client, err := vrpc.RPCDial("tcp", CONFIG.ClientAddr, logger, options)
	if err != nil {
		Logln("Failed to connect to notify client: ", CONFIG.ClientAddr, err.Error())
	} else {
		if client != nil {
			defer client.Close()
		}

		args := util.NotifyUnreachableArgs{UnreachableHostIP: strings.Split(hostAddr, ":")[0]}
		var reply bool

		err = client.Call("TerminalServices.NotifyHostUnreachable", args, &reply)
		if err != nil {
			Logln("Failed to notify client of unreachable host", err.Error())
		} else if reply {
			Logln("Notification successful!")
		} else {
			Logln("Unexpected response from client.")
		}
	}
}

// starts the stream connecting to the client
func (terminal *terminal_lib) StartMusic() string {
	result := ""
	if stream != nil {
		err := stream.Start()
		if err != nil {
			Logln("Chk StartMusic")
			chk(err)
			result = "Unable to start stream"
		} else {
			client, err := vrpc.RPCDial("tcp", CONFIG.ClientAddr, logger, options)

			if err != nil {
				Logln("Failed to connect: ", CONFIG.ClientAddr, err)

			} else {
				if client != nil {
					defer client.Close()
				}
			}
			args := util.GetSongArgs{CONFIG.TerminalID}
			var reply util.ReplySongArgs

			err = client.Call("TerminalServices.GetSongInfo", args, &reply)
			if err != nil {
				Logln("Failed to Retrieve Song Name ", err)
			}
			result = "NOW PLAYING: " + reply.SongName
		}
	} else {
		result = "Stream has not been initialized."
	}
	return result
}

func (terminal *terminal_lib) StopMusic() string {
	Logln("Chk StopMusic")
	chk(stream.Stop())
	return "Stopped Song"
}

func (terminal *terminal_lib) UploadSong() error {
	Logln("Attempting to connect to client to upload songs:", CONFIG.ClientAddr)
	client, err := vrpc.RPCDial("tcp", CONFIG.ClientAddr, logger, options)

	if err != nil {
		Logln("Failed to connect: ", CONFIG.ClientAddr, err)
		return err
	} else {
		if client != nil {
			defer client.Close()
		}

		songs, _ := ioutil.ReadDir(CONFIG.SongPath)

		for _, song := range songs {
			if !song.IsDir() {
				song_name := song.Name()
				if strings.ToLower(song_name[len(song_name)-3:]) == "mp3" {
					content, err := ioutil.ReadFile(CONFIG.SongPath + "/" + song_name)
					if err != nil {
						Logln("Failed to read song ", song_name, err)
					}

					song_hash := sha1.New()
					string_to_hash := song_name
					song_hash.Write([]byte(string_to_hash))
					hash_str := hex.EncodeToString(song_hash.Sum(nil))

					new_song := util.Song{song_name, "Universal", hash_str}
					add_new_song := util.AddTerminalSong{CONFIG.TerminalAddr, CONFIG.TerminalID, new_song, content}

					var reply bool

					err = client.Call("TerminalServices.AddSong", add_new_song, &reply)

					if err != nil {
						Logln("Failed to upload song ", song_name, " to", CONFIG.ClientAddr)
						continue
					} else if reply {
						Logln("Uploaded song ", song_name, " to client")
						continue
					} else {
						Logln(CONFIG.ClientAddr, "declined the add song request.")
						continue
					}
				}
			}

		}

	}
	return nil
}

func (terminal *terminal_lib) SkipSong() string {
	client, err := vrpc.RPCDial("tcp", CONFIG.ClientAddr, logger, options)

	if err != nil {
		Logln("Failed to connect: ", CONFIG.ClientAddr, err)
	} else {
		if client != nil {
			defer client.Close()
		}
	}
	args := util.TerminalArgs{CONFIG.TerminalID, CONFIG.TerminalAddr}
	var reply bool

	err = client.Call("TerminalServices.SkipCurrentSong", args, &reply)
	if err != nil {
		Logln("RPC call to skip song failed ", err.Error())
		return "Failed to skip song." + err.Error()
	}

	if reply {
		return "Skipped song."
	} else {
		return "Skip rejected."
	}
}

/////////////////////
// RPC Services
/////////////////////
func detectSwitchStream() {
	for {
		client, err := vrpc.RPCDial("tcp", CONFIG.ClientAddr, logger, options)
		if err != nil {
			Logln("Failed to connect to client: ", CONFIG.ClientAddr, err.Error())
		} else {
			if client != nil {
				defer client.Close()
			}

			args := util.TerminalArgs{CONFIG.TerminalID, CONFIG.TerminalAddr}
			var reply util.CurrentHostReply
			err = client.Call("TerminalServices.GetCurrentHost", args, &reply)
			if err != nil {
				Logln("RPC call to get current host failed ", err.Error())
			} else {
				latestHostAddr := reply.CurrentHost + ":" + reply.CurrentHostPort
				Logln("LastestHostAddr:", latestHostAddr)

				if latestHostAddr != getCurrentHostAddr() && reply.CurrentHost != "" {
					Logln(terminalInstance.StopMusic())
					//New stream has started
					Logln("Switch stream to", latestHostAddr)
					Logln("Song:", reply.CurrentSong)
					currentSong = reply.CurrentSong
					setCurrentHostAddr(latestHostAddr)
					portaudio.Terminate()
					Logln("Initialized PortAudio")

					terminalInstance.InitStream(latestHostAddr)
					terminalInstance.StartMusic()
				}
			}
		}
		time.Sleep(time.Second * 2)
	}
}

func (terminal *terminal_lib) GetCurrentSong() string {
	return currentSong + " " + getCurrentHostAddr()
}

// helper to check for vital errors that will panic the server
func chk(err error) bool {
	if err != nil {
		Logln("ERROR:", err.Error())
		return true
	}
	return false
}

func Logln(v ...interface{}) {
	logging.Println(v...)
}

func getIP(addr string) string {
	result := ""

	parts := strings.Split(addr, ":")
	if len(parts) == 2 {
		result = parts[0]
	}

	return result
}

func getCurrentHostAddr() string {
	currentHostAddrMutex.Lock()
	defer currentHostAddrMutex.Unlock()
	return currentHostAddr
}

func setCurrentHostAddr(hostAddr string) {
	currentHostAddrMutex.Lock()
	defer currentHostAddrMutex.Unlock()
	currentHostAddr = hostAddr
}
