package util

// a package for shared data structures between client-terminal

type InitMessage struct {
	TerminalID   string
	TerminalAddr string
	SongNum      int
}

type Song struct {
	Name  string
	Genre string
	Hash  string
}

// Sending Messages

type AddTerminalSong struct {
	PublicAddr string
	TerminalID string
	Song       Song
	Data       []byte
}

type ServeTerminalSong struct {
	TerminalAddr string
}

type SongStream struct {
	ClientID string
	Song     Song
	Data     []byte
}

type GetSongArgs struct {
	TerminalID string
}

// Reply Messages
type ReplySongArgs struct {
	SongName string
}

type TerminalArgs struct {
	TerminalID   string
	TerminalAddr string
}

type CurrentHostReply struct {
	CurrentHost     string
	CurrentHostPort string
	CurrentSong     string
}

type NotifyUnreachableArgs struct {
	UnreachableHostIP string
}

type TerminalInitReply struct {
	HostStreamAddr  string
	CurrentSongName string
}
