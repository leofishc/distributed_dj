package clientlib

import (
	"../util"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/DistributedClocks/GoVector/govec"
	"github.com/DistributedClocks/GoVector/govec/vrpc"
	"github.com/oandrew/go-mpg123"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ClientLib interface {
	JoinPeer(string) error
}

type clientlib struct {
}

type Config struct {
	ClientID              string
	PeerAddrs             []string
	PrivateIP             string
	PublicIP              string
	IncomingPort          string
	TerminalListeningAddr string
	SongPath              string
	SongListeningPort     string
}

type Peer struct {
	ClientID      string
	PublicIP      string
	Port          string
	StreamingPort string
}

var CONFIG Config
var logger *govec.GoLog
var options govec.GoLogOptions
var selfPublicIP string
var selfPublicIPWithPort string

// Terminal Stuff
var TerminalAddr string
var TerminalID string
var TerminalConnected = false
var printLogger *log.Logger
var terminalHasVoted = false

// Key: Song hash
var availableSongs = make(map[string]util.Song)
var availableSongsMutex = &sync.Mutex{}

// Key: Song hash
// Value: PeerIPAddr
var streamersForSongs = make(map[string][]string)
var streamersForSongsMutex = &sync.Mutex{}

// Key: Peer IP
var connectedPeers = make(map[string]Peer)
var connectedPeersMutex = &sync.Mutex{}

var currentHost string
var currentHostMutex = &sync.Mutex{}
var currentHostPort string
var currentHostStreamingPort string

var fileToStream string

var currentSong string
var currentSongMutex = &sync.Mutex{}

// Key: IPAddr
// Value: Song hash
var voteMap = make(map[string]string)
var voteMapMutex = &sync.Mutex{}

type Vote struct {
	songhash   string
	votestatus string
}

// Key: IPAddr
// Value: Vote Status
var pendingPrepares = make(map[string]Vote)
var pendingPreparesMutex = &sync.Mutex{}

func Initialize(configFile string) (ClientLib, error) {
	clearClientSongs()
	var client = &clientlib{}

	err := initConfig(configFile, &CONFIG)
	if err != nil {
		fmt.Println("initconfig", err.Error())
		return nil, err
	}

	logger = govec.InitGoVector(CONFIG.ClientID, CONFIG.ClientID, govec.GetDefaultConfig())
	options = govec.GetDefaultLogOptions()
	logger = logger
	options = options
	selfPublicIP = CONFIG.PublicIP
	var stringIPParts = []string{CONFIG.PublicIP, ":", CONFIG.IncomingPort}
	selfPublicIPWithPort = strings.Join(stringIPParts, "")
	printConfig()

	addDefaultSongs()

	//default song
	configureStream("glimmer.mp3")

	for _, peer := range CONFIG.PeerAddrs {
		client.JoinPeer(peer)
	}

	go client.startRPCClientServer()
	go client.startRPCTerminalServer()

	err = CastRandomVote()
	if err != nil {
		fmt.Println("Initialize - CastRandomVote", err.Error())
		return nil, err
	}

	if getCurrentHost() == "" {
		tallyVotesAndSetNextHost()
	}

	client.startStreamingServer()

	return client, nil
}

func tallyVotesAndSetNextHost() {
	fmt.Println("Tallying votes...")
	if currentHost == "" {
		//Tally the votes for each song hash
		songTally := make(map[string]int)
		highestNumVotes := 0
		songWithMostVotes := ""

		if len(voteMap) == 0 {
			availableSongsMutex.Lock()
			numAvailSongs := len(availableSongs)
			availableSongsMutex.Unlock()

			if numAvailSongs == 0 {
				fmt.Println("No votes found and no available songs. Resolving...")
				addDefaultSongs()
				CastRandomVote()
			} else {
				fmt.Println("No votes found but other songs are available. Resolving...")
				//Everyone voted for unreachable host so now there is no votes
				//But there are other songs available
				availableSongHashes := []string{}

				availableSongsMutex.Lock()
				for availSongHash := range availableSongs {
					availableSongHashes = append(availableSongHashes, availSongHash)
				}
				availableSongsMutex.Unlock()

				sort.Strings(availableSongHashes)
				selectedSongHash := availableSongHashes[0]

				availableSongsMutex.Lock()
				currentSong = availableSongs[selectedSongHash].Name
				availableSongsMutex.Unlock()

				streamersForSongsMutex.Lock()
				streamersForSelectedSong := streamersForSongs[selectedSongHash]
				streamersForSongsMutex.Unlock()

				newHostIP := setHostFromMultipleStreamers(streamersForSelectedSong)

				if newHostIP == selfPublicIP {
					fmt.Println("I am the host. Configuring stream...")
					configureStream(availableSongs[songWithMostVotes].Name)
					notifyPeersStartOfStream()
					CastRandomVote()
				}
				return
			}
		}

		voteMapMutex.Lock()
		fmt.Printf("%v\n", voteMap)
		for _, songHash := range voteMap {
			songTally[songHash]++
		}

		winningSongs := []string{}
		for songHash, tally := range songTally {
			if tally > highestNumVotes {
				highestNumVotes = tally
				winningSongs = []string{}
				winningSongs = append(winningSongs, songHash)
			} else if tally == highestNumVotes {
				highestNumVotes = tally
				winningSongs = append(winningSongs, songHash)
			}
		}

		sort.Strings(winningSongs)
		songWithMostVotes = winningSongs[0]
		
		voteMapMutex.Unlock()

		
		fmt.Println("WINNER:", availableSongs[songWithMostVotes].Name, "("+songWithMostVotes+")", "Votes:", highestNumVotes)
		

		streamersForSongsMutex.Lock()
		fmt.Printf("Streamers for all songs %v\n", streamersForSongs)
		streamers := streamersForSongs[songWithMostVotes]
		fmt.Printf("Streamers for song %v\n", streamers)
		streamersForSongsMutex.Unlock()

		var newHostIPSelected string
		if len(streamers) > 0 {
			currentSong = songWithMostVotes
			newHostIPSelected = setHostFromMultipleStreamers(streamers)
			fmt.Println("Host is set to:", getCurrentHost())
		} else {
		
			fmt.Println("ERROR: No streamers for song. Failed to set new streamer.")
		}

		if newHostIPSelected == selfPublicIP {
			fmt.Println("I am the host. Configuring stream...")
			configureStream(availableSongs[songWithMostVotes].Name)
			notifyPeersStartOfStream()
			CastRandomVote()
		}
	} else {
		fmt.Println("Cannot set next host. The current host is still streaming", getCurrentHost())
	}
}

func initConfig(configFile string, config *Config) error {
	configJson, err := os.Open(configFile)
	if err != nil {
		fmt.Println("initConfig", err)
		return err
	}
	defer configJson.Close()
	byteValue, _ := ioutil.ReadAll(configJson)

	json.Unmarshal(byteValue, config)
	return nil
}

func (client *clientlib) JoinPeer(peerAddr string) error {
	fmt.Println("Self Public IP is: ", selfPublicIPWithPort)

	if selfPublicIPWithPort == peerAddr {
		return errors.New("Cannot join Self IP")
	}

	fmt.Println("--> Attempting to join:", peerAddr)
	peer, err := vrpc.RPCDial("tcp", peerAddr, logger, options)
	if err != nil {
		fmt.Println("Failed to join:", peerAddr, "not accepting connections", err.Error())
		return err
	} else {
		if peer != nil {
			defer peer.Close()
		}

		availableSongsMutex.Lock()
		availSongs := availableSongs
		availableSongsMutex.Unlock()

		streamersForSongsMutex.Lock()
		streamers := streamersForSongs
		streamersForSongsMutex.Unlock()
		args := JoinArgs{PublicAddr: selfPublicIP, Port: CONFIG.IncomingPort, StreamingPort: CONFIG.SongListeningPort, ClientID: CONFIG.ClientID, AvailableSongs: availSongs, StreamersForSongs: streamers}

		var reply JoinReply

		logger.LogLocalEvent("Joining Peer: "+peerAddr, govec.GetDefaultLogOptions())

		err = peer.Call("PeerServices.Join", args, &reply)
		if err != nil {
			fmt.Println("Failed to join peer", peerAddr, err.Error())
			return err
		} else if reply.Joined {
			fmt.Println("Joined peer", peerAddr)
			fmt.Println("Received ", len(reply.AvailableSongs), " available songs")
			client.onJoinPeerSuccess(strings.Split(peerAddr, ":")[0], strings.Split(peerAddr, ":")[1], reply)

			for _, song := range reply.AvailableSongs {
				addAvailableSong(song)
			}
			return nil
		} else {
			fmt.Println(peerAddr, "declined the join request.")
			return nil
		}
	}
}

func (client *clientlib) onJoinPeerSuccess(peerIP string, peerPort string, reply JoinReply) {
	if !isConnectedToPeer(peerIP) && peerIP != CONFIG.PublicIP {
		addConnectedPeer(peerIP, peerPort, reply.ClientID, reply.ClientStreamingPort)
	}
	printConnectedPeers()

	setCurrentHost(reply.CurrentHost, reply.CurrentHostPort, reply.CurrentHostStreamingPort)
	currentSong = reply.CurrentSong

	fmt.Println("Received songs:", reply.AvailableSongs)
	for hash, song := range reply.AvailableSongs {
		availableSongsMutex.Lock()
		if _, songExists := availableSongs[hash]; !songExists {
			availableSongsMutex.Unlock()
			fmt.Println("Adding ", song.Name, " to our list")
			addAvailableSong(song)
		} else {
			availableSongsMutex.Unlock()
		}
	}

	fmt.Println("Adding streamers for songs", reply.StreamersForSongs)
	for songHash, incomingStreamersIPs := range reply.StreamersForSongs {
		streamersForSongsMutex.Lock()

		for _, ip := range incomingStreamersIPs {
			exists := false
			for _, currentStreamersIP := range streamersForSongs[songHash] {
				if currentStreamersIP == ip {
					exists = true
					break
				}
			}

			if !exists {
				streamersForSongs[songHash] = append(streamersForSongs[songHash], ip)
			}

		}

		streamersForSongsMutex.Unlock()
	}

	streamersForSongsMutex.Lock()
	streamersForSongs = reply.StreamersForSongs
	streamersForSongsMutex.Unlock()

	voteMapMutex.Lock()
	voteMap = reply.Votes
	voteMapMutex.Unlock()

	fmt.Println("Other peers to join:", reply.Peers)
	client.joinPeers(reply.Peers)
}

func (client *clientlib) joinPeers(peersToJoin map[string]Peer) {
	for _, peer := range peersToJoin {
		peerIP := peer.PublicIP
		if !isConnectedToPeer(peerIP) && peerIP != getSelfIPPort() {
			client.JoinPeer(peerIP + ":" + peer.Port)
		}
	}
}

func CastRandomVote() error {
	fmt.Println("Casting random vote.")

	availableSongsMutex.Lock()

	length := len(availableSongs)
	index := 0
	if length > 0 {
		index = rand.Intn(len(availableSongs))
	} else {
		availableSongsMutex.Unlock()
		fmt.Println("No inital songs to vote for..")
		return errors.New("songnotfound")
	}

	songhash := ""
	for hash := range availableSongs {
		if index == 0 {
			songhash = hash
			break
		}
		index--
	}
	availableSongsMutex.Unlock()

	fmt.Println("Voting for:", songhash)

	if len(getConnectedPeers()) > 0 {
		currentHostMutex.Lock()
		currHost := currentHost
		currHostPort := currentHostPort
		currentHostMutex.Unlock()

		currentHostWithPort := strings.Join([]string{currHost, ":", currHostPort}, "")
		fmt.Println("Sending Vote Request to host:", currentHostWithPort)
		c, err := vrpc.RPCDial("tcp", currentHostWithPort, logger, options)
		if err != nil {
			fmt.Println("Failure to send Vote Request, removing peer", err.Error())
			removePeer(currHost)
			fmt.Println("No host to connect to..")
			return errors.New("disconnectedhost")
		} else {
			defer c.Close()
			if len(songhash) == 0 {
				fmt.Println("Bad song hash..")
				return errors.New("songnotfound")

			}
			requestCommitArgs := RequestCommitArgs{selfPublicIP, CONFIG.ClientID, songhash, currentHost}
			var reqCommitReply RequestCommitReply

			err = c.Call("PeerServices.RequestCommit", requestCommitArgs, &reqCommitReply)
			if err != nil {
				fmt.Println("PeerServices.RequestCommit", err.Error())
				return err
			}
		}
	} else {
		voteMapMutex.Lock()
		voteMap[selfPublicIP] = songhash
		voteMapMutex.Unlock()
	}
	return nil
}

func removePeer(ip string) {
	removePeerWithPropagateOption(ip, true)
}

// Propagate set true if removePeer request hasn't flooded back to client. 
// Set false if second time seeing request

func removePeerWithPropagateOption(ip string, propagate bool) {
	connectedPeersMutex.Lock()
	currConnectedPeers := connectedPeers
	connectedPeersMutex.Unlock()

	if _, ok := currConnectedPeers[ip]; ok {
		fmt.Println("Remove peer", ip, "propagate:", propagate)
		connectedPeersMutex.Lock()
		delete(connectedPeers, ip)
		
		connectedPeersMutex.Unlock()
		var songsToDelete = []string{}
		// Find all songs hosted by peer removed

		streamersForSongsMutex.Lock()
		
		streamersForSongsCopy := streamersForSongs
		for songHash, streamers := range streamersForSongs {
			streamersCopy := streamers
			if len(streamers) == 1 {
				if streamers[0] == ip {
					delete(streamersForSongsCopy, songHash)
					streamersCopy = []string{}
					fmt.Println("Removed streamer from copy:", ip)
					songsToDelete = append(songsToDelete, songHash)
				}
			} else {
				for index, streamerIP := range streamers {
					if streamerIP == ip {
						if index == len(streamers)-1 {
							streamersCopy = streamers[:index]
						} else {
							streamersCopy = append(streamers[:index], streamers[index+1:]...)
						}
					}
				}
				// Update since it's not empty yet
				streamersForSongsCopy[songHash] = streamersCopy
			}
		}
		streamersForSongs = streamersForSongsCopy
		
		streamersForSongsMutex.Unlock()

		// Remove songs from availableSongs
		currHost := getCurrentHost()
		for _, songHash := range songsToDelete {

			availableSongsMutex.Lock()
			nameOfSongToDelete := availableSongs[songHash].Name
			delete(availableSongs, songHash)
			availableSongsMutex.Unlock()

			fmt.Println("Removed song:", nameOfSongToDelete, songHash)

			if voteMap[selfPublicIP] == songHash {
				fmt.Println("Song that we voted for is no longer available")
				//Can't cast vote if it is current host because it won't commit to other peers
				if ip != currHost && currHost != "" {
					CastRandomVote()
				}
			}
			// Remove votes for songs removed
			voteMapMutex.Lock()
			voteMapCopy := voteMap
			for ip, hash := range voteMapCopy {
				if hash == songHash {
					delete(voteMap, ip)
				}
			}
			voteMapMutex.Unlock()
		}

		// Remove votes from ip of peer being removed
		voteMapMutex.Lock()
		delete(voteMap, ip)
		voteMapMutex.Unlock()

		if propagate {
			for peerIP, peer := range getConnectedPeers() {
				fmt.Println("Propagate - Telling", peerIP, "to remove peer:", ip)
				client, err := vrpc.RPCDial("tcp", peerIP+":"+peer.Port, logger, options)
				if err != nil {
					fmt.Println("Could not reach", peerIP+":"+peer.Port, "to remove", ip, err.Error())
					removePeer(peerIP)
				} else {
					defer client.Close()
					removeArgs := RemoveArgs{selfPublicIPWithPort, ip}
					var reply bool

					err = client.Call("PeerServices.RemovePeer", removeArgs, &reply)
					if err != nil {
						fmt.Println("Error with RPC call to remove peer from", peerIP, err.Error())
					} else if reply {
						fmt.Println(peerIP, "removed peer", removeArgs.RemoveIP)
					} else {
						fmt.Println("Unexpected reply from peer", peerIP)
					}
				}
			}
		}

		if ip == currentHost || currHost == "" {
			setCurrentHost("", "", "")
			fmt.Println("Removed current host or current host is set to none already. Tallying votes...")
			tallyVotesAndSetNextHost()
		}
	}
}

func selectStreamerFromMultipleOptions(streamers []string) string {
	if len(streamers) == 1 {
		return streamers[0]
	} else {
		result := 0
		selectedIP := ""
		for _, streamerIP := range streamers {
			ipAsNumberString := strings.Replace(streamerIP, ".", "", -1)
			ipAsNumber, err := strconv.Atoi(ipAsNumberString)
			if err != nil {
				fmt.Println("Error choosing between peers. Cannot convert ipNumString to int", err.Error())
			} else if ipAsNumber > result || result == 0 {
				result = ipAsNumber
				selectedIP = streamerIP
			}
		}
		return selectedIP
	}
}

func setHostFromMultipleStreamers(streamers []string) string {
	newHostIP := selectStreamerFromMultipleOptions(streamers)
	if newHostIP == CONFIG.PublicIP {
		setCurrentHost(newHostIP, CONFIG.IncomingPort, CONFIG.SongListeningPort)
	} else {
		connectedPeersMutex.Lock()
		newHostPort := connectedPeers[newHostIP].Port
		newHostStreamingPort := connectedPeers[newHostIP].StreamingPort
		connectedPeersMutex.Unlock()

		setCurrentHost(newHostIP, newHostPort, newHostStreamingPort)
	}
	return newHostIP
}

/////////////////////
// Getters & Setters
/////////////////////
func getSelfIPPort() string {
	return CONFIG.PublicIP + ":" + CONFIG.IncomingPort
}

func getConnectedPeers() map[string]Peer {
	connectedPeersMutex.Lock()
	defer connectedPeersMutex.Unlock()
	return connectedPeers
}

func isConnectedToPeer(peerAddr string) bool {
	connectedPeersMutex.Lock()
	defer connectedPeersMutex.Unlock()
	_, ok := connectedPeers[peerAddr]
	return ok
}

func addConnectedPeer(peerIP string, peerPort string, peerId string, peerStreamingPort string) {
	connectedPeersMutex.Lock()
	defer connectedPeersMutex.Unlock()
	peer := Peer{peerId, peerIP, peerPort, peerStreamingPort}
	connectedPeers[peerIP] = peer
	fmt.Println("added connected peer:", peerIP)
}

func getCurrentHost() string {
	currentHostMutex.Lock()
	defer currentHostMutex.Unlock()
	return currentHost
}

func setCurrentHost(hostAddr string, hostPort string, streamingPort string) {
	fmt.Println("Setting host to:", hostAddr, "Port:", hostPort, "Streaming Port:", streamingPort)
	currentHostMutex.Lock()
	defer currentHostMutex.Unlock()
	currentHost = hostAddr
	currentHostPort = hostPort
	currentHostStreamingPort = streamingPort
	fmt.Println("Current host is set to:", hostAddr, "Port:", hostPort, "Streaming Port:", streamingPort)
}

func addAvailableSong(song util.Song) {
	availableSongsMutex.Lock()
	availableSongs[song.Hash] = song
	availableSongsMutex.Unlock()
}

/////////////////////

/////////////////////
// Prints
/////////////////////
func printConfig() {
	fmt.Println("--Configuration Initialized--")
	fmt.Println("Client ID:", CONFIG.ClientID)
	fmt.Println("Public Address:", CONFIG.PrivateIP)
	fmt.Println("Terminal Address:", CONFIG.TerminalListeningAddr)
	fmt.Println("Private IP:", CONFIG.PrivateIP)
	fmt.Println("Listening Client Port:", CONFIG.IncomingPort)
	fmt.Println("Streaming Song Port:", CONFIG.SongListeningPort)
	fmt.Println("Song Directory: ", CONFIG.SongPath)
	fmt.Println("")
}

func printConnectedPeers() {
	fmt.Println("Connected peers:", getConnectedPeers())
}

/////////////////////

/////////////////////
// RPC ARG STRUCTS
/////////////////////
type JoinArgs struct {
	PublicAddr        string
	Port              string
	ClientID          string
	StreamingPort     string
	AvailableSongs    map[string]util.Song
	StreamersForSongs map[string][]string
}

type AddSongArgs struct {
	PublicAddr string
	ClientID   string
	Song       util.Song
}

type RequestCommitArgs struct {
	VoterIP string
	//PublicAddr  string
	VoterID     string
	SongHash    string
	CurrentHost string
}

type PrepareArgs struct {
	PublicAddr  string
	ClientID    string
	VoterIP     string
	VoterID     string
	SongHash    string
	CurrentHost string
}

type VoteInfoArgs struct {
	TerminalID string
}

type VoteArgs struct {
	TerminalID string
	SongName   string
}

type NotifyArgs struct {
	SourceIP    string
	CurrentSong string
}
type AddTerminalSong struct {
	PublicAddr string
	TerminalID string
	Song       util.Song
	Data       []byte
}

type RemoveArgs struct {
	PublicAddr string
	RemoveIP   string
}

type StreamArgs struct {
	HostIPPort string
}

/////////////////////

/////////////////////
// RPC REPLY STRUCTS
/////////////////////
type JoinReply struct {
	Joined                   bool
	ClientID                 string
	ClientStreamingPort      string
	Peers                    map[string]Peer
	CurrentHost              string
	CurrentHostPort          string
	CurrentHostStreamingPort string
	CurrentSong              string
	AvailableSongs           map[string]util.Song
	StreamersForSongs        map[string][]string
	Votes                    map[string]string
	Err                      error
}

type RequestCommitReply struct {
	Error       error
	CurrentHost string
}

type CheckCommitReply struct {
	Status   string
	Voter    string
	SongHash string
}

type VoteInfoReply struct {
	Songs             map[string]util.Song
	CurrentHost       string
	Error             error
	StreamersForSongs map[string][]string
}

type VoteReply struct {
	Error error
}

type RemoveReply struct {
	PublicAddr string
	RemoveAddr string
	Error      error
}

/////////////////////

/////////////////////
// RPC Services
/////////////////////
type PeerServices struct{}

type TerminalServices struct{}

// Client - Client RPC Services
func (client *clientlib) startRPCClientServer() {
	// fmt.Println("Starting Client RPC server")
	logger := govec.InitGoVector(CONFIG.ClientID, CONFIG.ClientID, govec.GetDefaultConfig())
	peerServices := new(PeerServices)
	server := rpc.NewServer()
	server.Register(peerServices)

	localAddrBindForClient := CONFIG.PrivateIP + ":" + CONFIG.IncomingPort

	//fmt.Println("Listening on ", localAddrBindForClient, " for client calls")

	l, e := net.Listen("tcp", localAddrBindForClient)
	if e != nil {
		log.Fatal("listen error:", e)
	}
	options := govec.GetDefaultLogOptions()
	vrpc.ServeRPCConn(server, l, logger, options)
}

func (t *PeerServices) Join(args *JoinArgs, reply *JoinReply) error {
	addConnectedPeer(args.PublicAddr, args.Port, args.ClientID, args.StreamingPort)
	fmt.Println("<--", args.ClientID, "("+args.PublicAddr+")", "joined as peer!")
	printConnectedPeers()
	fmt.Println("Received", len(args.AvailableSongs), "available songs")
	fmt.Println("Replying with ", len(availableSongs), " available songs")
	for _, receivedsong := range args.AvailableSongs {
		addAvailableSong(receivedsong)
	}
	for _, song := range availableSongs {
		fmt.Println(song.Name)
	}
	fmt.Println("Adding streamers for songs", args.StreamersForSongs)
	for songHash, incomingStreamersIPs := range args.StreamersForSongs {
		streamersForSongsMutex.Lock()

		for _, ip := range incomingStreamersIPs {
			exists := false
			for _, currentStreamersIP := range streamersForSongs[songHash] {
				if currentStreamersIP == ip {
					exists = true
					break
				}
			}

			if !exists {
				streamersForSongs[songHash] = append(streamersForSongs[songHash], ip)
			}
		}
		streamersForSongsMutex.Unlock()
	}

	currentHostMutex.Lock()
	currHostStreamPort := currentHostStreamingPort
	currSong := currentSong
	currentHostMutex.Unlock()

	*reply = JoinReply{
		Joined:                   true,
		ClientID:                 CONFIG.ClientID,
		ClientStreamingPort:      CONFIG.SongListeningPort,
		Peers:                    getConnectedPeers(),
		CurrentHost:              getCurrentHost(),
		CurrentHostPort:          currentHostPort,
		CurrentHostStreamingPort: currHostStreamPort,
		CurrentSong:              currSong,
		AvailableSongs:           availableSongs,
		StreamersForSongs:        streamersForSongs,
		Votes:                    voteMap,
		Err:                      nil}
	return nil
}

func (t *PeerServices) AddSong(args *AddSongArgs, reply *bool) error {
	fmt.Println("<-- Received song from:", args.ClientID, args.Song)
	songHash := args.Song.Hash

	addAvailableSong(args.Song)

	streamersForSongsMutex.Lock()
	currentStreamersForSong := streamersForSongs[songHash]

	exists := false
	for _, peerIp := range currentStreamersForSong {
		if peerIp == args.PublicAddr {
			exists = true
			break
		}
	}

	if !exists {
		streamersForSongs[songHash] = append(currentStreamersForSong, args.PublicAddr)
	}
	streamersForSongsMutex.Unlock()

	*reply = true

	return nil
}

// VOTING RPC

func (t *PeerServices) RequestCommit(args *RequestCommitArgs, reply *RequestCommitReply) error {

	fmt.Println("Received commit request from:", args.VoterID, " at ", args.VoterIP)
	if getCurrentHost() == "" {
		*reply = RequestCommitReply{errors.New("End of song. Not accepting votes."), currentHost}
	} else if args.CurrentHost == selfPublicIP {
		done := make(chan bool)
		if _, songExists := availableSongs[args.SongHash]; songExists {
			fmt.Println("Executing 2PC..")

			go execute2PC(args, done)
		} else {
			fmt.Println("Song isn't available")
			*reply = RequestCommitReply{errors.New("invalidsong"), currentHost}
			return nil
		}

		val := <-done
		if val {
			fmt.Println("Replying to RequestCommit")
			*reply = RequestCommitReply{nil, currentHost}
		} else {
			fmt.Println("Replying with aborted..")
			*reply = RequestCommitReply{errors.New("aborted"), currentHost}
		}

	} else {
		*reply = RequestCommitReply{errors.New("invalidhost"), currentHost}
	}
	return nil
}

func execute2PC(args *RequestCommitArgs, done chan<- bool) {
	pendingPreparesMutex.Lock()
	pendingPrepares = make(map[string]Vote)
	pendingPreparesMutex.Unlock()

	clientStatus := make(map[string]bool)
	clientStatusMutex := &sync.Mutex{}
	abort := make(chan struct{})
	timeout := make(chan struct{})
	var clientIPs []string
	fmt.Println("Connected Peers: ", getConnectedPeers())

	for peerIPs := range getConnectedPeers() {
		clientIPs = append(clientIPs, peerIPs)
		clientStatusMutex.Lock()
		clientStatus[peerIPs] = false
		clientStatusMutex.Unlock()
	}
	clientIPs = append(clientIPs, selfPublicIP)
	//fmt.Println(clientStatus)
	fmt.Println("2PC with clientIPs:", clientIPs)
	clientStatusMutex.Lock()
	clientStatus[selfPublicIP] = false
	clientStatusMutex.Unlock()
	go waitForTimeout(timeout)
	for _, peerIP := range clientIPs {
		//fmt.Println("Started Timeout Timer")
		//fmt.Println("Sending Prepare Requests")
		//fmt.Println("A:",clientStatus)
		//fmt.Println("PeerIP", peerIP)

		peerAddr := peerIP
		if peerIP == selfPublicIP {
			peerAddr = selfPublicIPWithPort
		} else {
			connectedPeersMutex.Lock()
			peerAddr += ":" + connectedPeers[peerIP].Port
			connectedPeersMutex.Unlock()
		}
		go sendPrepareToCommit(clientStatus, clientStatusMutex, abort, peerIP, peerAddr, args)
	}

	isAborted := false

loop:
	for {
		select {
		case <-abort:
			fmt.Println("ABORT CALLED")
			isAborted = true
			//abort <- false
			go abort2PC(args.VoterIP, args.SongHash)
			break loop

		case <-timeout:
			fmt.Println("TIMEOUT CALLED")
			isAborted = true
			//abort <- false
			go abort2PC(args.VoterIP, args.SongHash)
			break loop

		default:
			allReady := true
			clientStatusMutex.Lock()
			for _, ready := range clientStatus {

				if !ready {
					allReady = false
				}
			}
			clientStatusMutex.Unlock()

			if allReady {
				fmt.Println("Everyone's prepared")
				break loop
			}

		}
	}

	if !isAborted {
		fmt.Println("Not aborted, committing")
		voteMapMutex.Lock()
		voteMap[args.VoterIP] = args.SongHash
		voteMapMutex.Unlock()
		clientStatusMutex.Lock()
		for client := range clientStatus {
			clientStatus[client] = false
		}
		clientStatusMutex.Unlock()
		pendingPreparesMutex.Lock()

		pendingPrepares[CONFIG.PublicIP] = Vote{args.SongHash, "committed"}

		pendingPreparesMutex.Unlock()
		//fmt.Println("Signalling 2PC finished")
		done <- true

		for _, peerIP := range clientIPs {
			//go waitForTimeout(abort)
			peerAddrWithPort := peerIP
			if peerIP == selfPublicIP {
				fmt.Println("selfpublicipwithport", selfPublicIPWithPort)
				peerAddrWithPort = selfPublicIPWithPort
			} else {
				connectedPeersMutex.Lock()
				peerAddrWithPort += ":" + connectedPeers[peerIP].Port
				connectedPeersMutex.Unlock()
			}
			go sendCommit(clientStatus, abort, peerAddrWithPort, args)
		}
	}
	done <- false
}

func sendPrepareToCommit(clientStatus map[string]bool, clientStatusMutex *sync.Mutex, abort chan struct{}, peerIP string, peerAddr string, args *RequestCommitArgs) {
	fmt.Println("Sending prepare to commit request to:", peerAddr)
	clientStatusMutex.Lock()
	clientStatus[peerIP] = false
	clientStatusMutex.Unlock()
	client, err := vrpc.RPCDial("tcp", peerAddr, logger, options)
	if err != nil {
		fmt.Println("Failure to send commit request, removing peer", peerIP, err.Error())
		removePeer(peerIP)
	} else {
		defer client.Close()
		prepareArgs := PrepareArgs{selfPublicIPWithPort, CONFIG.ClientID, args.VoterIP, args.VoterID, args.SongHash, currentHost}
		var reply RequestCommitReply

		err = client.Call("PeerServices.PrepareToCommit", prepareArgs, &reply)
		if err != nil {
			fmt.Println("A", err.Error())
			close(abort)

		} else {
			// Abort will be in error
			//fmt.Println("sendPrepareToCommit-connectedpeers", connectedPeers)
			if reply.Error != nil {
				clientStatusMutex.Lock()
				clientStatus[peerIP] = false
				//fmt.Println("sendPrepareToCommit-", peerIP, "false")
				clientStatusMutex.Unlock()
				fmt.Println("Aborted by", args.VoterID)
				close(abort)
			} else {
				clientStatusMutex.Lock()
				clientStatus[peerIP] = true
				//fmt.Println("sendPrepareToCommit-", peerIP, "true")
				//fmt.Println("sendPrepareToCommit", clientStatus)
				clientStatusMutex.Unlock()
				fmt.Println(peerIP, " is prepared")
			}
		}
	}
}

func sendCommit(clientStatus map[string]bool, abort chan struct{}, peerAddrWithPort string, args *RequestCommitArgs) {
	fmt.Println("Sending commit to", peerAddrWithPort, "The vote was from", args.VoterID, args.VoterIP)
	client, err := vrpc.RPCDial("tcp", peerAddrWithPort, logger, options)
	if err != nil {
		defer client.Close()
		fmt.Println("Failure to send commit, removing peer", err.Error())
		removePeer(strings.Split(peerAddrWithPort, ":")[0])
	} else {
		prepareArgs := PrepareArgs{selfPublicIPWithPort, CONFIG.ClientID, args.VoterIP, args.VoterID, args.SongHash, currentHost}
		var reply RequestCommitReply

		err = client.Call("PeerServices.Commit", prepareArgs, &reply)
		if err != nil {
			fmt.Println("PeerServices.Commit", err.Error())
		}
	}
}

func (t *PeerServices) PrepareToCommit(args *PrepareArgs, reply *RequestCommitReply) error {

	fmt.Println("Received prepare to commit request from:", args.ClientID)

	//if currentHost == args.PublicAddr {
	availableSongsMutex.Lock()
	defer availableSongsMutex.Unlock()
	if _, songExists := availableSongs[args.SongHash]; songExists {
		fmt.Println("Song Exists..")
		pendingPreparesMutex.Lock()
		pendingPrepares[args.VoterIP] = Vote{args.SongHash, "prepared"}
		pendingPreparesMutex.Unlock()
		go waitToCheckCommit(args)
		fmt.Println("Sending prepared to ", args.ClientID)
		*reply = RequestCommitReply{nil, currentHost}
		return nil
	} else {
		fmt.Println("Song doesn't exist", args.SongHash, "Vote from:", args.VoterID, args.VoterIP)
		*reply = RequestCommitReply{errors.New("invalidsong"), currentHost}
		return nil
	}

	//}

	*reply = RequestCommitReply{errors.New("invalidhost"), currentHost}

	return nil
}

func waitToCheckCommit(args *PrepareArgs) {

	time.Sleep(3 * time.Second)
	//pendingPreparesMutex.Lock()
	if pendingPrepares[CONFIG.PublicIP].votestatus != "committed" {
		checkOtherPeersForCommit(args)
	}
	//pendingPreparesMutex.Unlock()
}

func checkOtherPeersForCommit(args *PrepareArgs) {
	fmt.Println("checkOtherPeersForCommit", args)
	for peerIP, peer := range getConnectedPeers() {
		checkPeerCommit(peerIP+":"+peer.Port, args)
	}
	if voteMap[args.VoterIP] != args.SongHash {
		abort2PC(args.VoterIP, args.SongHash)
	}
}

func checkPeerCommit(peerAddrWithPort string, args *PrepareArgs) {
	fmt.Println("check peer commit", peerAddrWithPort)
	client, err := vrpc.RPCDial("tcp", peerAddrWithPort, logger, options)
	if err != nil {
		fmt.Println("Failure to check commit, removing peer", err.Error())
		removePeer(strings.Split(peerAddrWithPort, ":")[0])
	} else {
		defer client.Close()
		prepareArgs := PrepareArgs{selfPublicIP, CONFIG.ClientID, args.VoterIP, args.VoterID, args.SongHash, currentHost}
		var reply CheckCommitReply

		err = client.Call("PeerServices.CheckCommit", prepareArgs, &reply)
		if err != nil {
			fmt.Println("error checking commit from", peerAddrWithPort, err.Error())
		} else {
			fmt.Println(peerAddrWithPort, "status:", reply.Status)
			if reply.Status == "committed" {
				voteMapMutex.Lock()
				voteMap[args.VoterIP] = args.SongHash
				voteMapMutex.Unlock()
			}
		}
	}
}

func (t *PeerServices) CheckCommit(args *PrepareArgs, reply *CheckCommitReply) error {
	pendingPreparesMutex.Lock()
	defer pendingPreparesMutex.Unlock()
	*reply = CheckCommitReply{pendingPrepares[args.VoterIP].votestatus, args.VoterIP, pendingPrepares[args.VoterIP].songhash}
	return nil
}

func (t *PeerServices) Commit(args *PrepareArgs, reply *RequestCommitReply) error {
	fmt.Println("Committing Vote")
	voteMapMutex.Lock()
	voteMap[args.VoterIP] = args.SongHash
	voteMapMutex.Unlock()
	fmt.Println(voteMap)
	pendingPreparesMutex.Lock()
	vote := Vote{args.SongHash, "committed"}
	pendingPrepares[args.VoterIP] = vote
	pendingPreparesMutex.Unlock()

	availableSongsMutex.Lock()
	songName := availableSongs[args.SongHash]
	availableSongsMutex.Unlock()

	fmt.Println("Vote committed:", songName, vote)
	voteMapMutex.Lock()
	fmt.Println("All votes committed:", voteMap)
	voteMapMutex.Unlock()
	*reply = RequestCommitReply{nil, currentHost}
	return nil
}

func (t *PeerServices) RemovePeer(args *RemoveArgs, reply *bool) error {
	fmt.Println("Received request from", args.PublicAddr, "to remove", args.RemoveIP)
	removePeerWithPropagateOption(args.RemoveIP, false)
	*reply = true
	return nil
}

func waitForTimeout(timeout chan struct{}) {
	time.Sleep(3 * time.Second)
	//fmt.Println("Sending timeout abort signal")
	if !isClosed(timeout) {
		close(timeout)
	}
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
	}
	return false
}

func waitForCheck(check chan<- bool) {
	time.Sleep(5 * time.Second)
	check <- true
}

func abort2PC(voterIP string, songHash string) {
	fmt.Println("Aborting 2PC!")
	pendingPreparesMutex.Lock()
	delete(pendingPrepares, voterIP)
	pendingPreparesMutex.Unlock()
}

func (t *PeerServices) NotifyEndOfStream(args *NotifyArgs, reply *bool) error {
	if args.SourceIP == getCurrentHost() {
		fmt.Println("Notified of end of stream from", args.SourceIP)
		setCurrentHost("", "", "")
		currentSong = args.CurrentSong
		tallyVotesAndSetNextHost()
	}

	*reply = true
	return nil
}

func (t *PeerServices) NotifyStartOfStream(args *NotifyArgs, reply *bool) error {
	fmt.Println("Notified of start of stream from", args.SourceIP, args.CurrentSong)
	currentSong = args.CurrentSong

	connectedPeersMutex.Lock()
	newHostPeer := connectedPeers[args.SourceIP]
	connectedPeersMutex.Unlock()

	setCurrentHost(newHostPeer.PublicIP, newHostPeer.Port, newHostPeer.StreamingPort)
	CastRandomVote()

	*reply = true
	return nil
}

func notifyPeersEndOfStream() {
	peers := getConnectedPeers()
	fmt.Println("Notifying peers of end of stream:", peers)
	for _, peer := range peers {
		peerAddr := peer.PublicIP + ":" + peer.Port
		fmt.Println("Notifying peers:", peerAddr)
		client, err := vrpc.RPCDial("tcp", peerAddr, logger, options)
		if err != nil {
			fmt.Println("Failure to notify end of stream, removing peer", err.Error())
			removePeer(peer.PublicIP)
		} else {
			defer client.Close()
			notifyArgs := NotifyArgs{selfPublicIP, ""}
			var reply bool
			err = client.Call("PeerServices.NotifyEndOfStream", notifyArgs, &reply)
			if err != nil {
				fmt.Println("RPC call to NotifyEndOfStream failed:", err.Error())
			} else {
				if reply {
					fmt.Println("Notified", peer.ClientID, "("+peerAddr+")", "that the stream has ended.")
				} else {
					fmt.Println("Unexpected response from peer:", peer.ClientID, "("+peerAddr+")")
				}
			}
		}
	}
	fmt.Println("Finished notifying peers of end of stream")
}

func notifyPeersStartOfStream() {
	peers := getConnectedPeers()
	fmt.Println("Notifying peers of start of stream:", peers)
	for _, peer := range peers {
		peerAddr := peer.PublicIP + ":" + peer.Port
		fmt.Println("Notifying peers:", peerAddr)
		client, err := vrpc.RPCDial("tcp", peerAddr, logger, options)
		if err != nil {
			fmt.Println("Failure to notify start of song, removing peer", err.Error())
			removePeer(peer.PublicIP)
		} else {
			defer client.Close()
			notifyArgs := NotifyArgs{selfPublicIP, currentSong}
			var reply bool

			err = client.Call("PeerServices.NotifyStartOfStream", notifyArgs, &reply)
			if err != nil {
				fmt.Println("RPC call to NotifyEndOfStream failed:", err.Error())
			} else {
				if reply {
					fmt.Println("Notified", peer.ClientID, "("+peerAddr+")", "that the stream has started.")
				} else {
					fmt.Println("Unexpected response from peer:", peer.ClientID, "("+peerAddr+")")
				}
			}
		}
	}
	fmt.Println("Finished notifying peers of start of stream")
}

const sampleRate = 44100

var reader *mpg123.Reader
var numSamples = sampleRate
var samples = make([]int16, numSamples)
var cachedSamples = make([]int16, numSamples)
var lastSampleNum int

func configureStream(fileName string) {
	fileToStream = fileName
	currentSong = fileName

	cfg := mpg123.ReaderConfig{
		OutputFormat: &mpg123.OutputFormat{
			Channels: 1,
			Rate:     44100,
			Encoding: mpg123.EncodingInt16,
		},
	}

	SongPath := CONFIG.SongPath + fileToStream
	file, err := os.Open(SongPath)
	if err != nil {
		fmt.Println("Failed to open file", SongPath, err.Error())
		return
	}
	reader = mpg123.NewReaderConfig(file, cfg)

	fmt.Println("--- Stream Info: ---")
	fmt.Printf("Song Name: %s\n", currentSong)
	fmt.Printf("Rate: %d | Channels %d \n", cfg.OutputFormat.Rate, cfg.OutputFormat.Channels)

	//Clear samples from previous song
	samples = make([]int16, numSamples)
	cachedSamples = make([]int16, numSamples)
}

// Streaming Server
func (client *clientlib) startStreamingServer() {
	//Parse metadata
	_, err := reader.Read(nil)
	if err != nil {
		fmt.Println("Reader error, stream may not have been setup.", err.Error())
	} else {
		fmt.Printf("Format: %+v\n", reader.OutputFormat())
		fmt.Printf("FrameInfo: %+v\n", reader.FrameInfo())
		fmt.Printf("Meta ID3v2:  %#v\n", reader.Meta().ID3v2)

		http.HandleFunc("/cacheaudio", func(w http.ResponseWriter, r *http.Request) {
			flusher, ok := w.(http.Flusher)
			if !ok {
				panic("expected http.ResponseWriter to be an http.Flusher")
			}

			w.Header().Set("Connection", "Keep-Alive")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Transfer-Encoding", "chunked")
			w.Header().Set("Content-Type", "audio/mpeg")

			//log.Printf("Offset: %+v", reader.Offset())
			err := binary.Read(reader, binary.LittleEndian, samples)
			if err == io.EOF {
				fmt.Println("End of song.")
				nextSong()
			} else {
				binary.Write(w, binary.LittleEndian, &samples)
				go func() {
					cachedSamples = make([]int16, numSamples)
					copy(cachedSamples, samples)
				}()
			}
			flusher.Flush() // Trigger "chunked" encoding and send a chunk...
		})

		http.HandleFunc("/audio", func(w http.ResponseWriter, r *http.Request) {
			flusher, ok := w.(http.Flusher)
			if !ok {
				panic("expected http.ResponseWriter to be an http.Flusher")
			}

			w.Header().Set("Connection", "Keep-Alive")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Transfer-Encoding", "chunked")
			w.Header().Set("Content-Type", "audio/mpeg")

			binary.Write(w, binary.LittleEndian, &cachedSamples)

			flusher.Flush() // Trigger "chunked" encoding and send a chunk...
		})

		chk(http.ListenAndServe(":"+CONFIG.SongListeningPort, nil))
	}
}

func nextSong() {
	fmt.Println("Next Song")
	setCurrentHost("", "", "")
	currentSong = ""
	notifyPeersEndOfStream()
	tallyVotesAndSetNextHost()
	fmt.Println("Next Song selected")
}

// Client - Terminal RPC Services
func (client *clientlib) startRPCTerminalServer() {

	logger := govec.InitGoVector(CONFIG.ClientID, "RPC Terminal Server", govec.GetDefaultConfig())
	TerminalConnected = false

	terminalServices := new(TerminalServices)
	server := rpc.NewServer()
	server.Register(terminalServices)

	localAddrForTerminal := CONFIG.TerminalListeningAddr

	l, e := net.Listen("tcp", localAddrForTerminal)
	if e != nil {
		log.Fatal("terminal listen error:", e)
	}
	options := govec.GetDefaultLogOptions()
	printLogger = log.New(os.Stdout, "[Terminal-Service-Logs]", log.Lshortfile)
	vrpc.ServeRPCConn(server, l, logger, options)
}

func (t *TerminalServices) AddSong(args *AddTerminalSong, reply *bool) error {
	printTerminalLog("Received Song " + args.Song.Name)
	songHash := args.Song.Hash

	if _, ok := availableSongs[songHash]; !ok {

		addAvailableSong(args.Song)

		streamersForSongsMutex.Lock()
		streamersForSongs[songHash] = append(streamersForSongs[songHash], selfPublicIP)
		streamersForSongsMutex.Unlock()

		err := ioutil.WriteFile(CONFIG.SongPath+args.Song.Name, args.Data, 0644)

		if err != nil {
			printTerminalLog("Error Adding Song " + args.Song.Name + err.Error())
		}
	} else {
		printTerminalLog("Song: " + args.Song.Name + " already available, did not upload")
	}
	go propagateSongToPeers(args.Song)

	*reply = true
	return nil
}

func propagateSongToPeers(song util.Song) {
	for _, peer := range CONFIG.PeerAddrs {
		go sendSong(peer, song)
	}
}

func sendSong(peerAddrWithPort string, song util.Song) {
	fmt.Println("Sending song to", peerAddrWithPort, song)
	client, err := vrpc.RPCDial("tcp", peerAddrWithPort, logger, options)
	if err != nil {
		fmt.Println("Failure to send song, removing peer", err.Error())
		removePeer(strings.Split(peerAddrWithPort, ":")[0])
	} else {
		defer client.Close()
		addArgs := AddSongArgs{selfPublicIP, CONFIG.ClientID, song}
		var reply bool

		err = client.Call("PeerServices.AddSong", addArgs, &reply)
		if err != nil {
			fmt.Println("Failed to add ", song.Name, " to ", peerAddrWithPort, err.Error())
		} else {
			if reply {
				fmt.Println("Added ", song.Name, " to ", peerAddrWithPort)
			}
		}
	}
}

func (t *TerminalServices) GetSongInfo(args *util.GetSongArgs, reply *util.ReplySongArgs) error {
	*reply = util.ReplySongArgs{currentSong}
	return nil
}

func (t *TerminalServices) GetVotingInfo(args *VoteInfoArgs, reply *VoteInfoReply) error {
	streamersForSongsMutex.Lock()
	streamersSongs := streamersForSongs
	streamersForSongsMutex.Unlock()
	*reply = VoteInfoReply{availableSongs, getCurrentHost(), nil, streamersSongs}
	return nil

}

func (t *TerminalServices) CastVote(args *VoteArgs, reply *VoteReply) error {
	currentHostMutex.Lock()
	currHost := currentHost
	currHostPort := currentHostPort
	currentHostMutex.Unlock()

	fmt.Println("Casting vote from terminal", args.TerminalID, "for song", args.SongName, "to host", currHost+":"+currHostPort)
	client, err := vrpc.RPCDial("tcp", currHost+":"+currHostPort, logger, options)
	if err != nil {
		fmt.Println("Failure to Cast Vote, removing peer", err.Error())
		removePeer(currHost)
		*reply = VoteReply{errors.New("disconnectedhost")}
		return nil
	} else {
		defer client.Close()
		songHash := findSongHash(args.SongName)
		if len(songHash) == 0 {
			*reply = VoteReply{errors.New("songnotfound")}
			return nil
		}
		requestCommitArgs := RequestCommitArgs{selfPublicIP, CONFIG.ClientID, songHash, currentHost}
		var reqCommitReply RequestCommitReply
		fmt.Println(reqCommitReply)

		err = client.Call("PeerServices.RequestCommit", requestCommitArgs, &reply)
		if err != nil {
			fmt.Println("Failed to make RPC call to request commit", err.Error())
		} else if reply.Error != nil {
			fmt.Println("Returning to terminal with error!")
			*reply = VoteReply{errors.New("votenotcast")}
			return nil
		} else {
			terminalHasVoted = true
			*reply = VoteReply{nil}
			return nil
		}
	}
	terminalHasVoted = true
	*reply = VoteReply{nil}
	return nil

}

func findSongHash(songName string) string {
	for hash, song := range availableSongs {
		if songName == song.Name {
			return hash
		}
	}
	return ""
}

func (t *TerminalServices) Init(args *util.InitMessage, reply *util.TerminalInitReply) error {

	if !TerminalConnected || (TerminalID == args.TerminalID && TerminalAddr == args.TerminalAddr) {
		TerminalConnected = true
		TerminalID = args.TerminalID
		TerminalAddr = args.TerminalAddr
		printTerminalLog("Successfully Established Connection")

		currentHostMutex.Lock()
		currHostStreamPort := currentHostStreamingPort
		currSong := currentSong
		currentHostMutex.Unlock()

		*reply = util.TerminalInitReply{HostStreamAddr: getCurrentHost() + ":" + currHostStreamPort, CurrentSongName: currSong}
		return nil
	} else {
		printLogger.Println("Warning: Trying To Establish Another Connection --> ", args.TerminalID, args.TerminalAddr)
		return errors.New("Client is already paired with a Terminal: " + TerminalAddr)
	}

}

func (t *TerminalServices) SkipCurrentSong(args *util.TerminalArgs, reply *bool) error {
	fmt.Println("Received request to skip current song.")
	if currentHost == selfPublicIP {
		nextSong()
		*reply = true
		fmt.Println("Reply true")
	} else {
		fmt.Println("Only the current host can skip the current song.")
		*reply = false
		fmt.Println("Reply false")
	}
	fmt.Println("Return from skip current song.")
	return nil
}

func (t *TerminalServices) GetCurrentHost(args *util.TerminalArgs, reply *util.CurrentHostReply) error {
	
	currentHostMutex.Lock()
	currHostStreamPort := currentHostStreamingPort
	currSong := currentSong
	currentHostMutex.Unlock()

	*reply = util.CurrentHostReply{CurrentHost: getCurrentHost(), CurrentHostPort: currHostStreamPort, CurrentSong: currSong}
	return nil
}

func (t *TerminalServices) NotifyHostUnreachable(args *util.NotifyUnreachableArgs, reply *bool) error {
	fmt.Println("Received notification that the current host is unreachable.", args.UnreachableHostIP)
	removePeer(args.UnreachableHostIP)

	*reply = true
	return nil
}

func clearClientSongs() {
	songs, _ := ioutil.ReadDir(CONFIG.SongPath)

	for _, song := range songs {
		if !song.IsDir() {
			song_name := song.Name()
			if song_name != "glimmer.mp3" {
				os.Remove(CONFIG.SongPath + song_name)
				fmt.Println("removed" + song_name)
			}
		}
	}
}

func addDefaultSongs() {
	fmt.Println("Adding default songs.")
	songs, _ := ioutil.ReadDir(CONFIG.SongPath)

	for _, song := range songs {
		if !song.IsDir() {
			song_name := song.Name()
			if strings.ToLower(song_name[len(song_name)-3:]) == "mp3" {
				hash := sha1.New()
				string_to_hash := song_name
				hash.Write([]byte(string_to_hash))
				songHash := hex.EncodeToString(hash.Sum(nil))

				new_song := util.Song{song_name, "Universal", songHash}

				if _, ok := availableSongs[songHash]; !ok {
					fmt.Println("Adding", song_name, "Hash:", songHash)
					addAvailableSong(new_song)

					streamersForSongsMutex.Lock()
					streamersForSongs[songHash] = append(streamersForSongs[songHash], selfPublicIP)
					streamersForSongsMutex.Unlock()

					go propagateSongToPeers(new_song)
				}
			}
		}
	}
}

// helper to print to terminal log, keeps prints consistent so its not messy af
func printTerminalLog(message string) {
	printLogger.Println(message + " --> " + TerminalID)
}

// helper to check for vital errors that should panic the server
func chk(err error) {
	if err != nil {
		fmt.Println(err)
	}
}
