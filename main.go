package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/pterm/pterm"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

const refreshInterval = 500 * time.Millisecond

type arguments struct {
	magnetLink         string
	torrentPath        string
	saveDir            string
	uploadOnly         bool
	uploadPath         string
	gdriveUpload       bool
	gdriveCredentials  string
	gdriveClientID     string
	gdriveClientSecret string
	gdriveToken        string
	gdriveParentID     string
	gdrivePrintAuth    bool
}

func main() {
	if err := run(); err != nil {
		pterm.Error.Printfln("%v", err)
		os.Exit(1)
	}
}

func run() error {
	args, err := parseArgs(os.Args[1:])
	if err != nil {
		return err
	}

	if args.uploadOnly {
		return runUploadOnly(args)
	}

	if args.gdrivePrintAuth && !args.gdriveUpload {
		return runDriveAuthOnly(args)
	}

	args, err = promptForTorrentSource(args)
	if err != nil {
		return err
	}

	client, err := initializeClient(args.saveDir)
	if err != nil {
		return err
	}
	clientClosed := false
	defer func() {
		if !clientClosed {
			client.Close()
		}
	}()

	var torrents []*torrent.Torrent
	if args.magnetLink == "" && args.torrentPath == "" {
		torrents, err = loadSavedTorrents(client, args.saveDir)
		if err != nil {
			return err
		}
		if len(torrents) == 0 {
			return errors.New("no input provided and no saved torrents found to resume")
		}
		pterm.Info.Printfln("Loaded %d saved torrent(s) from %s", len(torrents), sessionDir(args.saveDir))
	} else {
		tor, err := addTorrent(client, args)
		if err != nil {
			return err
		}
		torrents = append(torrents, tor)
		pterm.Info.Printfln("Added torrent: %s", safeTorrentName(tor))
		pterm.Info.Printfln("Info hash: %s", tor.InfoHash().HexString())
	}

	pterm.Info.Printfln("Waiting for metadata...")
	for _, tor := range torrents {
		<-tor.GotInfo()
		if tor.Info() == nil {
			return fmt.Errorf("torrent metadata could not be loaded for %s", safeTorrentName(tor))
		}
		if err := persistTorrent(tor, args.saveDir); err != nil {
			return err
		}
	}

	startDownloads(client)

	if len(torrents) == 1 {
		pterm.Success.Printfln("Metadata loaded for %s", torrents[0].Name())
	} else {
		pterm.Success.Printfln("Metadata loaded for %d torrents", len(torrents))
	}
	pterm.Info.Printfln("Starting download into %s", args.saveDir)

	if err := monitorProgress(client); err != nil {
		return err
	}

	client.Close()
	clientClosed = true
	pterm.Info.Println("Download finished. Torrent client closed, seeding stopped.")

	if args.gdriveUpload {
		if err := uploadCompletedTorrentsToDrive(args, torrents); err != nil {
			return err
		}
	}

	return nil
}

func runUploadOnly(args arguments) error {
	if args.uploadPath == "" {
		return errors.New("provide --upload-path when using --upload-only")
	}
	if !args.gdriveUpload {
		return errors.New("use --gdrive-upload with --upload-only")
	}

	uploadPath, err := filepath.Abs(args.uploadPath)
	if err != nil {
		return fmt.Errorf("resolve upload path: %w", err)
	}
	if _, err := os.Stat(uploadPath); err != nil {
		return fmt.Errorf("access upload path: %w", err)
	}

	if args.gdrivePrintAuth {
		if err := runDriveAuthOnly(args); err != nil {
			return err
		}
	}

	return uploadPathsToDrive(args, []string{uploadPath})
}

func runDriveAuthOnly(args arguments) error {
	ctx := context.Background()
	_, authInfo, err := newDriveService(ctx, args)
	if err != nil {
		return err
	}
	printReusableDriveAuth(authInfo)
	return nil
}

func promptForTorrentSource(args arguments) (arguments, error) {
	if args.magnetLink != "" || args.torrentPath != "" {
		return args, nil
	}

	pterm.Info.Println("Enter a magnet URL or torrent file path. Leave it empty to resume saved torrents.")
	fmt.Print("> ")

	reader := bufio.NewReader(os.Stdin)
	rawInput, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return arguments{}, fmt.Errorf("read torrent input: %w", err)
	}

	value := strings.TrimSpace(rawInput)
	if value == "" {
		return args, nil
	}

	if strings.HasPrefix(value, "magnet:") {
		args.magnetLink = value
		return args, nil
	}

	args.torrentPath = value
	return args, nil
}

func parseArgs(input []string) (arguments, error) {
	args := arguments{
		saveDir: "./Downloads",
	}

	for i := 0; i < len(input); i++ {
		switch input[i] {
		case "-m":
			if i+1 >= len(input) {
				return arguments{}, errors.New("missing magnet link after -m")
			}
			args.magnetLink = input[i+1]
			i++
		case "-t":
			if i+1 >= len(input) {
				return arguments{}, errors.New("missing torrent path after -t")
			}
			args.torrentPath = input[i+1]
			i++
		case "-o":
			if i+1 >= len(input) {
				return arguments{}, errors.New("missing output directory after -o")
			}
			args.saveDir = input[i+1]
			i++
		case "--upload-only":
			args.uploadOnly = true
		case "--upload-path":
			if i+1 >= len(input) {
				return arguments{}, errors.New("missing path after --upload-path")
			}
			args.uploadPath = input[i+1]
			i++
		case "--gdrive-upload":
			args.gdriveUpload = true
		case "--gdrive-credentials":
			if i+1 >= len(input) {
				return arguments{}, errors.New("missing credentials path after --gdrive-credentials")
			}
			args.gdriveCredentials = input[i+1]
			i++
		case "--gdrive-client-id":
			if i+1 >= len(input) {
				return arguments{}, errors.New("missing client id after --gdrive-client-id")
			}
			args.gdriveClientID = input[i+1]
			i++
		case "--gdrive-client-secret":
			if i+1 >= len(input) {
				return arguments{}, errors.New("missing client secret after --gdrive-client-secret")
			}
			args.gdriveClientSecret = input[i+1]
			i++
		case "--gdrive-token":
			if i+1 >= len(input) {
				return arguments{}, errors.New("missing token json after --gdrive-token")
			}
			args.gdriveToken = input[i+1]
			i++
		case "--gdrive-parent-id":
			if i+1 >= len(input) {
				return arguments{}, errors.New("missing parent id after --gdrive-parent-id")
			}
			args.gdriveParentID = input[i+1]
			i++
		case "--gdrive-print-auth":
			args.gdrivePrintAuth = true
		case "-h", "--help":
			printUsage()
			os.Exit(0)
		default:
			return arguments{}, fmt.Errorf("unknown argument: %s", input[i])
		}
	}

	if args.magnetLink != "" && args.torrentPath != "" {
		return arguments{}, errors.New("use either -m or -t, not both")
	}

	if args.gdriveClientID != "" && args.gdriveClientSecret == "" {
		return arguments{}, errors.New("provide --gdrive-client-secret when using --gdrive-client-id")
	}
	if args.gdriveClientSecret != "" && args.gdriveClientID == "" {
		return arguments{}, errors.New("provide --gdrive-client-id when using --gdrive-client-secret")
	}

	return args, nil
}

func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  torrent_cli -m <magnet-link> [-o <download-dir>]")
	fmt.Println("  torrent_cli -t <torrent-file> [-o <download-dir>]")
	fmt.Println("  torrent_cli [-o <download-dir>] [--gdrive-upload]")
	fmt.Println("  torrent_cli --upload-only --upload-path <file-or-folder> --gdrive-upload")
	fmt.Println("")
	fmt.Println("Google Drive:")
	fmt.Println("  --upload-only")
	fmt.Println("  --upload-path <file-or-folder>")
	fmt.Println("  --gdrive-upload")
	fmt.Println("  --gdrive-credentials <credentials.json>")
	fmt.Println("  --gdrive-client-id <id>")
	fmt.Println("  --gdrive-client-secret <secret>")
	fmt.Println("  --gdrive-token <token-json>")
	fmt.Println("  --gdrive-parent-id <drive-folder-id>")
	fmt.Println("  --gdrive-print-auth")
}

func initializeClient(saveDir string) (*torrent.Client, error) {
	if err := os.MkdirAll(saveDir, 0o750); err != nil {
		return nil, fmt.Errorf("create download directory: %w", err)
	}

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = saveDir
	cfg.Seed = false

	client, err := torrent.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("initialize torrent client: %w", err)
	}

	return client, nil
}

func addTorrent(client *torrent.Client, args arguments) (*torrent.Torrent, error) {
	if args.torrentPath != "" {
		tor, err := client.AddTorrentFromFile(args.torrentPath)
		if err != nil {
			return nil, fmt.Errorf("add torrent file: %w", err)
		}
		return tor, nil
	}

	tor, err := client.AddMagnet(args.magnetLink)
	if err != nil {
		return nil, fmt.Errorf("add magnet link: %w", err)
	}
	return tor, nil
}

func loadSavedTorrents(client *torrent.Client, saveDir string) ([]*torrent.Torrent, error) {
	entries, err := os.ReadDir(sessionTorrentsDir(saveDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read saved torrents: %w", err)
	}

	var torrentFiles []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".torrent") {
			continue
		}
		torrentFiles = append(torrentFiles, filepath.Join(sessionTorrentsDir(saveDir), entry.Name()))
	}
	sort.Strings(torrentFiles)

	var torrents []*torrent.Torrent
	for _, torrentFile := range torrentFiles {
		tor, err := client.AddTorrentFromFile(torrentFile)
		if err != nil {
			return nil, fmt.Errorf("load saved torrent %s: %w", torrentFile, err)
		}
		torrents = append(torrents, tor)
	}

	return torrents, nil
}

func persistTorrent(tor *torrent.Torrent, saveDir string) error {
	if err := os.MkdirAll(sessionTorrentsDir(saveDir), 0o750); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}

	metainfoPath := filepath.Join(sessionTorrentsDir(saveDir), tor.InfoHash().HexString()+".torrent")
	file, err := os.Create(metainfoPath)
	if err != nil {
		return fmt.Errorf("create metainfo file: %w", err)
	}
	defer file.Close()

	mi := tor.Metainfo()
	if err := mi.Write(file); err != nil {
		return fmt.Errorf("write metainfo file: %w", err)
	}

	return nil
}

func sessionDir(saveDir string) string {
	return filepath.Join(saveDir, ".torrent-cli")
}

func sessionTorrentsDir(saveDir string) string {
	return filepath.Join(sessionDir(saveDir), "torrents")
}

func driveTokenPath(saveDir string) string {
	return filepath.Join(sessionDir(saveDir), "gdrive-token.json")
}

func startDownloads(client *torrent.Client) {
	for _, tor := range client.Torrents() {
		if tor.Info() == nil {
			continue
		}

		for _, file := range tor.Files() {
			file.Download()
		}
	}
}

func monitorProgress(client *torrent.Client) error {
	multi, err := pterm.DefaultMultiPrinter.WithUpdateDelay(150 * time.Millisecond).Start()
	if err != nil {
		return fmt.Errorf("start terminal UI: %w", err)
	}
	defer multi.Stop()

	headerPrinter, err := pterm.DefaultSpinner.WithWriter(multi.NewWriter()).Start("Preparing download view")
	if err != nil {
		return fmt.Errorf("start header printer: %w", err)
	}

	var overallBar *pterm.ProgressbarPrinter
	filePrinters := make(map[string]*pterm.ProgressbarPrinter)
	fileLabels := make(map[string]string)
	lastStats := client.Stats()
	lastTick := time.Now()

	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	for {
		allComplete := true
		haveTorrentInfo := false
		var totalBytes int64
		var completedBytes int64

		stats := client.Stats()
		now := time.Now()
		intervalSeconds := now.Sub(lastTick).Seconds()
		if intervalSeconds <= 0 {
			intervalSeconds = refreshInterval.Seconds()
		}

		downloadRate := float64(stats.BytesReadUsefulData.Int64()-lastStats.BytesReadUsefulData.Int64()) / intervalSeconds
		uploadRate := float64(stats.BytesWrittenData.Int64()-lastStats.BytesWrittenData.Int64()) / intervalSeconds

		for _, tor := range client.Torrents() {
			if tor.Info() == nil {
				allComplete = false
				continue
			}

			haveTorrentInfo = true
			torrentKey := tor.InfoHash().HexString()
			torrentTotalBytes := tor.Length()
			torrentCompletedBytes := tor.BytesCompleted()

			totalBytes += torrentTotalBytes
			completedBytes += torrentCompletedBytes

			if torrentCompletedBytes < torrentTotalBytes {
				allComplete = false
			}

			if _, exists := fileLabels[torrentKey]; !exists {
				for key, label := range buildFileLabels(tor) {
					fileLabels[key] = label
				}
			}

			for _, file := range tor.Files() {
				fileKey := torrentKey + ":" + file.Path()
				fileBar, exists := filePrinters[fileKey]
				if !exists {
					fileBar, err = newProgressBar(multi.NewWriter(), file.Length(), "  File: "+truncateLabel(fileLabels[fileKey], 44))
					if err != nil {
						return fmt.Errorf("start file progress bar: %w", err)
					}
					filePrinters[fileKey] = fileBar
				}

				fileCompleted := file.BytesCompleted()
				updateProgressBar(
					fileBar,
					fileCompleted,
					file.Length(),
					fmt.Sprintf(
						"  File: %s  %s / %s",
						truncateLabel(fileLabels[fileKey], 34),
						toReadableSize(fileCompleted),
						toReadableSize(file.Length()),
					),
				)
			}
		}

		if !haveTorrentInfo {
			headerPrinter.UpdateText("Waiting for torrent metadata")
		} else {
			headerPrinter.UpdateText("Downloading torrent data")
		}

		if haveTorrentInfo {
			if overallBar == nil {
				overallBar, err = newProgressBar(multi.NewWriter(), totalBytes, "Folder progress")
				if err != nil {
					return fmt.Errorf("start overall progress bar: %w", err)
				}
			}

			updateProgressBar(
				overallBar,
				completedBytes,
				totalBytes,
				fmt.Sprintf(
					"Folder: %s / %s | ETA %s | DL %s/s | UL %s/s",
					toReadableSize(completedBytes),
					toReadableSize(totalBytes),
					formatETA(totalBytes-completedBytes, downloadRate),
					toReadableSize(int64(downloadRate)),
					toReadableSize(int64(uploadRate)),
				),
			)
		}

		if allComplete && haveTorrentInfo {
			headerPrinter.Success("All files downloaded")
			if overallBar != nil {
				_, _ = overallBar.Stop()
			}
			for _, bar := range filePrinters {
				_, _ = bar.Stop()
			}
			return nil
		}

		lastStats = stats
		lastTick = now
		<-ticker.C
	}
}

func uploadCompletedTorrentsToDrive(args arguments, torrents []*torrent.Torrent) error {
	var roots []string
	seen := make(map[string]struct{})
	for _, tor := range torrents {
		rootPath := filepath.Join(args.saveDir, filepath.FromSlash(tor.Name()))
		if _, exists := seen[rootPath]; exists {
			continue
		}
		seen[rootPath] = struct{}{}
		roots = append(roots, rootPath)
	}
	return uploadPathsToDrive(args, roots)
}

type driveUploadTracker struct {
	totalFiles  int
	completed   int
	totalBytes  int64
	uploaded    int64
	currentFile string
	currentSize int64
	overallBar  *pterm.ProgressbarPrinter
	currentBar  *pterm.ProgressbarPrinter
	status      *pterm.SpinnerPrinter
}

type driveAuthInfo struct {
	clientID     string
	clientSecret string
	token        *oauth2.Token
}

func uploadPathsToDrive(args arguments, roots []string) error {
	ctx := context.Background()
	driveService, authInfo, err := newDriveService(ctx, args)
	if err != nil {
		return err
	}

	if args.gdrivePrintAuth {
		printReusableDriveAuth(authInfo)
	}

	sort.Strings(roots)

	totalFiles, totalBytes, err := collectUploadWork(roots)
	if err != nil {
		return err
	}

	multi, err := pterm.DefaultMultiPrinter.WithUpdateDelay(150 * time.Millisecond).Start()
	if err != nil {
		return fmt.Errorf("start Google Drive upload UI: %w", err)
	}
	defer multi.Stop()

	statusPrinter, err := pterm.DefaultSpinner.WithWriter(multi.NewWriter()).Start("Preparing Google Drive upload")
	if err != nil {
		return fmt.Errorf("start Google Drive status spinner: %w", err)
	}

	overallBar, err := newProgressBar(multi.NewWriter(), totalBytes, "Google Drive: preparing upload")
	if err != nil {
		return fmt.Errorf("start Google Drive overall progress bar: %w", err)
	}

	currentFileBar, err := newProgressBar(multi.NewWriter(), 1, "Current file: waiting")
	if err != nil {
		return fmt.Errorf("start Google Drive file progress bar: %w", err)
	}

	tracker := &driveUploadTracker{
		totalFiles:  totalFiles,
		overallBar:  overallBar,
		currentBar:  currentFileBar,
		status:      statusPrinter,
		totalBytes:  totalBytes,
		currentFile: "waiting",
	}

	parentID := args.gdriveParentID
	for _, rootPath := range roots {
		info, err := os.Stat(rootPath)
		if err != nil {
			return fmt.Errorf("stat completed path %s: %w", rootPath, err)
		}

		statusPrinter.UpdateText("Uploading " + filepath.Base(rootPath) + " to Google Drive")
		if info.IsDir() {
			driveRootID, err := createDriveFolder(ctx, driveService, filepath.Base(rootPath), parentID)
			if err != nil {
				return err
			}
			if err := uploadDirectoryToDrive(ctx, driveService, rootPath, driveRootID, tracker); err != nil {
				return err
			}
		} else {
			if err := uploadFileToDrive(ctx, driveService, rootPath, parentID, tracker); err != nil {
				return err
			}
		}
	}

	statusPrinter.Success("Google Drive upload completed")
	_, _ = overallBar.Stop()
	_, _ = currentFileBar.Stop()
	return nil
}

func newDriveService(ctx context.Context, args arguments) (*drive.Service, driveAuthInfo, error) {
	config, clientID, clientSecret, err := loadDriveOAuthConfig(args)
	if err != nil {
		return nil, driveAuthInfo{}, err
	}

	token, err := loadDriveToken(args, config)
	if err != nil {
		return nil, driveAuthInfo{}, err
	}

	httpClient := config.Client(ctx, token)
	service, err := drive.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, driveAuthInfo{}, fmt.Errorf("create Google Drive service: %w", err)
	}

	return service, driveAuthInfo{
		clientID:     clientID,
		clientSecret: clientSecret,
		token:        token,
	}, nil
}

func loadDriveOAuthConfig(args arguments) (*oauth2.Config, string, string, error) {
	if args.gdriveClientID != "" && args.gdriveClientSecret != "" {
		config := &oauth2.Config{
			ClientID:     args.gdriveClientID,
			ClientSecret: args.gdriveClientSecret,
			Endpoint:     google.Endpoint,
			RedirectURL:  "urn:ietf:wg:oauth:2.0:oob",
			Scopes:       []string{drive.DriveFileScope},
		}
		return config, args.gdriveClientID, args.gdriveClientSecret, nil
	}

	credentialsPath := args.gdriveCredentials
	if credentialsPath == "" {
		credentialsPath = "credentials.json"
	}

	credentialsJSON, err := os.ReadFile(credentialsPath)
	if err != nil {
		return nil, "", "", fmt.Errorf("read Google Drive credentials file: %w", err)
	}

	config, err := google.ConfigFromJSON(credentialsJSON, drive.DriveFileScope)
	if err != nil {
		return nil, "", "", fmt.Errorf("parse Google Drive credentials: %w", err)
	}

	return config, config.ClientID, config.ClientSecret, nil
}

func loadDriveToken(args arguments, config *oauth2.Config) (*oauth2.Token, error) {
	if args.gdriveToken != "" {
		token, err := tokenFromJSON(args.gdriveToken)
		if err != nil {
			return nil, fmt.Errorf("parse --gdrive-token: %w", err)
		}
		return token, nil
	}

	tokenPath := driveTokenPath(args.saveDir)
	token, err := tokenFromFile(tokenPath)
	if err == nil {
		return token, nil
	}

	token = getTokenFromWeb(config)
	if err := saveToken(tokenPath, token); err != nil {
		return nil, err
	}
	return token, nil
}

func getTokenFromWeb(config *oauth2.Config) *oauth2.Token {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	pterm.Info.Printfln("Open this link, approve access, then paste the authorization code:")
	fmt.Println(authURL)

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		panic(fmt.Errorf("read authorization code: %w", err))
	}

	token, err := config.Exchange(context.Background(), authCode)
	if err != nil {
		panic(fmt.Errorf("exchange authorization code: %w", err))
	}
	return token
}

func tokenFromFile(filePath string) (*oauth2.Token, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	token := &oauth2.Token{}
	if err := json.NewDecoder(file).Decode(token); err != nil {
		return nil, err
	}
	return token, nil
}

func tokenFromJSON(raw string) (*oauth2.Token, error) {
	token := &oauth2.Token{}
	if err := json.Unmarshal([]byte(raw), token); err != nil {
		return nil, err
	}
	return token, nil
}

func saveToken(path string, token *oauth2.Token) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create token directory: %w", err)
	}

	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open token file: %w", err)
	}
	defer file.Close()

	if err := json.NewEncoder(file).Encode(token); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}
	return nil
}

func printReusableDriveAuth(auth driveAuthInfo) {
	if auth.token == nil {
		return
	}

	rawToken, err := json.Marshal(auth.token)
	if err != nil {
		pterm.Warning.Printfln("Could not print reusable Google Drive auth: %v", err)
		return
	}

	pterm.Success.Println("Reusable Google Drive auth:")
	fmt.Printf("client_id=%s\n", auth.clientID)
	fmt.Printf("client_secret=%s\n", auth.clientSecret)
	fmt.Printf("token=%s\n", string(rawToken))
	fmt.Printf(
		"reuse args: --gdrive-client-id %s --gdrive-client-secret %s --gdrive-token %s\n",
		strconv.Quote(auth.clientID),
		strconv.Quote(auth.clientSecret),
		strconv.Quote(string(rawToken)),
	)
}

func createDriveFolder(ctx context.Context, service *drive.Service, name, parentID string) (string, error) {
	file := &drive.File{
		Name:     name,
		MimeType: "application/vnd.google-apps.folder",
	}
	if parentID != "" {
		file.Parents = []string{parentID}
	}

	created, err := service.Files.Create(file).Context(ctx).Fields("id").Do()
	if err != nil {
		return "", fmt.Errorf("create Google Drive folder %s: %w", name, err)
	}
	return created.Id, nil
}

func collectUploadWork(roots []string) (int, int64, error) {
	totalFiles := 0
	var totalBytes int64

	for _, rootPath := range roots {
		err := filepath.Walk(rootPath, func(currentPath string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() {
				return nil
			}
			totalFiles++
			totalBytes += info.Size()
			return nil
		})
		if err != nil {
			return 0, 0, fmt.Errorf("scan upload work for %s: %w", rootPath, err)
		}
	}

	return totalFiles, totalBytes, nil
}

func uploadDirectoryToDrive(ctx context.Context, service *drive.Service, rootPath, parentID string, tracker *driveUploadTracker) error {
	folders := map[string]string{
		rootPath: parentID,
	}

	return filepath.Walk(rootPath, func(currentPath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if currentPath == rootPath {
			return nil
		}

		parentPath := filepath.Dir(currentPath)
		driveParentID := folders[parentPath]

		if info.IsDir() {
			folderID, err := createDriveFolder(ctx, service, info.Name(), driveParentID)
			if err != nil {
				return err
			}
			folders[currentPath] = folderID
			return nil
		}

		return uploadFileToDrive(ctx, service, currentPath, driveParentID, tracker)
	})
}

func uploadFileToDrive(ctx context.Context, service *drive.Service, filePath, parentID string, tracker *driveUploadTracker) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open file for upload %s: %w", filePath, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat file for upload %s: %w", filePath, err)
	}
	size := info.Size()
	tracker.startFile(filePath, size)

	driveFile := &drive.File{
		Name: filepath.Base(filePath),
	}
	if parentID != "" {
		driveFile.Parents = []string{parentID}
	}

	mediaType := mime.TypeByExtension(filepath.Ext(filePath))
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}

	var lastUploaded int64
	call := service.Files.Create(driveFile).
		ResumableMedia(ctx, file, size, mediaType).
		ProgressUpdater(func(current, total int64) {
			_ = total
			tracker.advance(current - lastUploaded)
			lastUploaded = current
		})

	_, err = call.Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("upload file %s to Google Drive: %w", filePath, err)
	}
	tracker.finishFile(size)
	return nil
}

func (t *driveUploadTracker) startFile(filePath string, size int64) {
	t.currentFile = filepath.Base(filePath)
	t.currentSize = size
	t.currentBar.Total = max(safeInt(size), 1)
	t.currentBar.Current = 0
	t.currentBar.UpdateTitle(fmt.Sprintf(
		"Current file: %s  %s / %s",
		truncateLabel(t.currentFile, 40),
		toReadableSize(0),
		toReadableSize(size),
	))
	t.updateOverallTitle()
	t.status.UpdateText("Uploading " + t.currentFile + " to Google Drive")
}

func (t *driveUploadTracker) advance(delta int64) {
	if delta < 0 {
		return
	}
	t.uploaded += delta
	t.currentBar.Current = min(t.currentBar.Current+safeInt(delta), t.currentBar.Total)
	t.currentBar.UpdateTitle(fmt.Sprintf(
		"Current file: %s  %s / %s",
		truncateLabel(t.currentFile, 40),
		toReadableSize(int64(t.currentBar.Current)),
		toReadableSize(int64(t.currentBar.Total)),
	))
	t.updateOverallTitle()
}

func (t *driveUploadTracker) finishFile(size int64) {
	if remaining := size - int64(t.currentBar.Current); remaining > 0 {
		t.advance(remaining)
	}
	t.completed++
	t.currentBar.Current = max(safeInt(size), 1)
	t.currentBar.UpdateTitle(fmt.Sprintf(
		"Current file: %s  %s / %s",
		truncateLabel(t.currentFile, 40),
		toReadableSize(size),
		toReadableSize(size),
	))
	t.updateOverallTitle()
}

func (t *driveUploadTracker) updateOverallTitle() {
	t.overallBar.Total = max(safeInt(t.totalBytes), 1)
	t.overallBar.Current = min(safeInt(t.uploaded), t.overallBar.Total)
	t.overallBar.UpdateTitle(fmt.Sprintf(
		"Google Drive: %s / %s | Files %d/%d",
		toReadableSize(t.uploaded),
		toReadableSize(t.totalBytes),
		t.completed,
		t.totalFiles,
	))
}

func newProgressBar(writer io.Writer, total int64, title string) (*pterm.ProgressbarPrinter, error) {
	return pterm.DefaultProgressbar.
		WithWriter(writer).
		WithTotal(safeInt(total)).
		WithShowCount(false).
		WithShowElapsedTime(true).
		WithTitle(title).
		Start()
}

func updateProgressBar(bar *pterm.ProgressbarPrinter, current, total int64, title string) {
	bar.Total = max(safeInt(total), 1)
	bar.Current = min(safeInt(current), bar.Total)
	bar.UpdateTitle(title)
}

func safeTorrentName(tor *torrent.Torrent) string {
	name := tor.Name()
	if strings.TrimSpace(name) != "" {
		return name
	}
	return tor.InfoHash().HexString()
}

func truncateLabel(value string, maxLen int) string {
	if maxLen <= 3 || len(value) <= maxLen {
		return value
	}
	return value[:maxLen-3] + "..."
}

func buildFileLabels(tor *torrent.Torrent) map[string]string {
	labels := make(map[string]string)
	bases := make([]string, 0, len(tor.Files()))

	for _, file := range tor.Files() {
		bases = append(bases, path.Base(file.DisplayPath()))
	}

	commonPrefix := commonWordPrefix(bases)

	for _, file := range tor.Files() {
		fileKey := tor.InfoHash().HexString() + ":" + file.Path()
		displayPath := file.DisplayPath()
		dir := path.Dir(displayPath)
		base := path.Base(displayPath)
		trimmedBase := trimSharedPrefix(base, commonPrefix)

		if dir == "." || dir == "/" || dir == "" {
			labels[fileKey] = trimmedBase
			continue
		}

		labels[fileKey] = dir + "/" + trimmedBase
	}

	return labels
}

func commonWordPrefix(values []string) string {
	if len(values) < 2 {
		return ""
	}

	firstWords := strings.Fields(values[0])
	if len(firstWords) == 0 {
		return ""
	}

	prefixLen := 0
	for prefixLen < len(firstWords) {
		candidate := firstWords[prefixLen]
		for _, value := range values[1:] {
			words := strings.Fields(value)
			if prefixLen >= len(words) || words[prefixLen] != candidate {
				return strings.Join(firstWords[:prefixLen], " ")
			}
		}
		prefixLen++
	}

	return strings.Join(firstWords[:prefixLen], " ")
}

func trimSharedPrefix(value, prefix string) string {
	if prefix == "" {
		return value
	}

	trimmed := strings.TrimSpace(strings.TrimPrefix(value, prefix))
	trimmed = strings.TrimLeft(trimmed, "-_. ")
	if trimmed == "" {
		return value
	}
	return trimmed
}

func toReadableSize(bytes int64) string {
	if bytes <= 0 {
		return "0 B"
	}

	suffix := []string{"B", "KB", "MB", "GB", "TB"}
	index := int(math.Floor(math.Log(float64(bytes)) / math.Log(1024)))
	if index >= len(suffix) {
		index = len(suffix) - 1
	}

	divisor := math.Pow(1024, float64(index))
	value := float64(bytes) / divisor
	return fmt.Sprintf("%.2f %s", value, suffix[index])
}

func formatETA(remainingBytes int64, bytesPerSecond float64) string {
	if remainingBytes <= 0 {
		return "done"
	}
	if bytesPerSecond <= 0 {
		return "--"
	}

	seconds := int64(math.Ceil(float64(remainingBytes) / bytesPerSecond))
	duration := time.Duration(seconds) * time.Second

	if duration >= time.Hour {
		return duration.Round(time.Second).String()
	}
	if duration >= time.Minute {
		return duration.Round(time.Second).String()
	}
	return duration.String()
}

func safeInt(value int64) int {
	maxInt := int64(^uint(0) >> 1)
	if value > maxInt {
		return int(maxInt)
	}
	if value < 0 {
		return 0
	}
	return int(value)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func init() {
	pterm.ThemeDefault.ProgressbarBarStyle = *pterm.NewStyle(pterm.FgLightCyan)
	pterm.ThemeDefault.ProgressbarTitleStyle = *pterm.NewStyle(pterm.FgLightWhite)
	pterm.SetDefaultOutput(os.Stdout)
	pterm.EnableStyling()
	http.DefaultTransport.(*http.Transport).MaxIdleConnsPerHost = 10
}
