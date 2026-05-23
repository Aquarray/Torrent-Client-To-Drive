package main

import (
	"fmt"
	"math"
	"os"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

var TorrentData map[string]*metainfo.Info = make(map[string]*metainfo.Info)

func main() {
	args := parseArgs()
	cli := intialize_Client(args.save_dir)
	defer cli.Close()
	var err error
	var torr *torrent.Torrent
	if args.torrent_path != "" {
		Log("Main", "Adding Torrent")
		torr, err = cli.AddTorrentFromFile(args.torrent_path)
		Log("Info", "Adding "+torr.Name()+" ; Hash: "+torr.InfoHash().HexString())
	} else if args.magnetLink != "" {
		Log("Main", "Adding Magnet Link")
		// torr, err = torrent.TorrentSpecFromMagnetUri(args.magnetLink)
		torr, err = cli.AddMagnet(args.magnetLink)
		Log("Info", "Adding "+torr.Name()+" ; Hash: "+torr.InfoHash().HexString())
	}
	if err != nil {
		Log("Error", err)
	}
	startHandShaking(cli)
	StartDownloadingAll(cli)
	MonitorProgress(cli)
}

type Arguement struct {
	magnetLink   string
	torrent_path string
	save_dir     string `default:"./Downloads"`
}

func parseArgs() Arguement {
	var res Arguement

	for i, x := range os.Args {
		switch x {
		case "-m":
			res.magnetLink = os.Args[i+1]
		case "-t":
			res.torrent_path = os.Args[i+1]
		case "-o":
			res.save_dir = os.Args[i+1]
		}
	}
	return res
}

func intialize_Client(save_dir string) *torrent.Client {
	cfg := torrent.NewDefaultClientConfig()
	_, err := os.Stat(save_dir)
	if os.IsNotExist(err) {
		os.Mkdir(save_dir, 0750)
	}
	cfg.DataDir = save_dir
	// cb := torrent.Callbacks{
	// 	CompletedHandshake: OnHandShakeCompleted,
	// }
	// cfg.Callbacks = cb

	cli, err := torrent.NewClient(cfg)
	if err != nil {
		Log("Client Intializing", err)
	}
	return cli
}

/*
func addMagnet(,link string) {

}

func addTorrent(path string) {
	file, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		Log("Adding Torrent", err)
	}

	bT := parseTorrent(file)

	Log("Parsing Torrent Result", bT.AnnounceList)
}
*/

func Log(from string, data any) {
	fmt.Printf("%v : %v\n", from, data)
}

/*
type bencodeInfo struct {
	Pieces      string `bencode:"pieces"`
	PieceLength int    `bencode:"piece length"`
	Length      int    `bencode:"length"`
	Name        string `bencode:"name"`
}

type bencodeTorrent struct {
	Announce     string      `bencode:"announce"`
	AnnounceList [][]string  `bencode:"announce-list"`
	Info         bencodeInfo `bencode:"info"`
}

func parseTorrent(data []byte) bencodeTorrent {
	var bT bencodeTorrent
	err := bencode.Decode(data, &bT)
	if err != nil {
		Log("Torrent Parsing", err)
	}
	return bT
}

type bencodeTrackerResp struct {
    Interval       int    `bencode:"interval"`
    Peers          string `bencode:"peers"`
    FailureReason  string `bencode:"failure reason,omitempty"`
    WarningMessage string `bencode:"warning message,omitempty"`
    Complete       int    `bencode:"complete,omitempty"`
    Incomplete     int    `bencode:"incomplete,omitempty"`
}

func checkTrackers(){

}


func startDownloading(save_dir string) {
	_, err := os.Stat(save_dir)

	if os.IsNotExist(err) {
		os.Mkdir(save_dir, 0750)
	}

}
*/

func startHandShaking(cli *torrent.Client) {
	for _, tor := range cli.Torrents() {
		Log("Info", "Handshaking with Torrent Info Hash"+tor.InfoHash().HexString())
		<-tor.GotInfo()
		TorrentData[tor.InfoHash().HexString()] = tor.Info()
	}
}

func StartDownloadingAll(cli *torrent.Client) {
	for _, tor := range cli.Torrents() {
		if tor.Info() != nil {
			Log("Info", "Downloading "+tor.InfoHash().HexString())
			tor.DownloadAll()
		} else {
			Log("Erro", "Handshaking with "+tor.InfoHash().HexString()+" not Found")
		}
	}

}

func MonitorProgress(cli *torrent.Client) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		for _, tor := range cli.Torrents() {
			fmt.Printf("Progress : %v -> %v / %v\n", tor.InfoHash().HexString(), ConvertToReadableForm(tor.BytesCompleted()), ConvertToReadableForm(tor.Length()))
		}
	}
}

func ConvertToReadableForm(bytes int64) string {
	if bytes == 0 {
		return "0B"
	}
	suffix := []string{"B", "KB", "MB", "GB", "TB"}
	i := int64(math.Floor(math.Log(float64(bytes)) / math.Log(1024)))
	p := math.Pow(1024, float64(i))
	s := (float64(bytes) / p)
	return fmt.Sprintf("%.2f %v", s, suffix[i])
}

// func OnHandShakeCompleted(pc *torrent.PeerConn, hash torrent.InfoHash) {
// 	fmt.Printf("Info : Handshaking Completed for %v with %v\n", pc.Torrent().Info().BestName(), pc.RemoteAddr)
// }

// func OnPeerConnClosed(pc *torrent.PeerConn) {
// 	fmt.Println("Info : " + "HandShake Closed " + pc.String())
// }

// func OnSentRequest(event torrent.PeerRequestEvent) {
// 	fmt.Println("Info : " + "Request sent to " + event.Peer.Torrent().Name())
// }

// func OnStatusUpdated(event torrent.StatusUpdatedEvent) {
// 	fmt.Println("Info : " + "Updated event" + event.Url)
// }
