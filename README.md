# Torrent Drive CLI

A Go command-line app for downloading torrents from magnet links or `.torrent` files, resuming saved sessions, and optionally uploading completed files to Google Drive.


## Features

- Download from a magnet link or local `.torrent` file
- Resume previously saved torrents automatically
- Show live folder and file progress in the terminal
- Stop seeding after the download finishes
- Upload completed files or folders to Google Drive
- Reuse Google Drive auth data for later uploads

## Tech Stack

- Go
- [`anacrolix/torrent`](https://github.com/anacrolix/torrent)
- [`pterm`](https://github.com/pterm/pterm)
- Google Drive API

## Project Structure

```text
.
├── main.go
├── go.mod
└── go.sum
```

## Getting Started

### Requirements

- Go 1.26+

### Build

```bash
go build -o torrent_cli .
```

### Run

Download from a magnet link:

```bash
./torrent_cli -m "magnet:?xt=urn:btih:..."
```

Download from a `.torrent` file:

```bash
./torrent_cli -t ./example.torrent
```

Choose a custom download directory:

```bash
./torrent_cli -m "magnet:?xt=urn:btih:..." -o ./Downloads
```

Resume saved torrents from the download directory:

```bash
./torrent_cli -o ./Downloads
```

If no torrent input is passed, the app also supports an interactive prompt where you can paste a magnet link, enter a torrent file path, or leave it empty to resume saved torrents.

## Google Drive Upload

After a download finishes, upload the completed files to Google Drive:

```bash
./torrent_cli -m "magnet:?xt=urn:btih:..." --gdrive-upload
```

Upload an existing file or folder without downloading:

```bash
./torrent_cli --upload-only --upload-path /path/to/folder --gdrive-upload
```

Use a specific OAuth credentials file:

```bash
./torrent_cli --upload-only --upload-path /path/to/folder --gdrive-upload --gdrive-credentials ./credentials.json
```

Target a specific Google Drive folder:

```bash
./torrent_cli --upload-only --upload-path /path/to/folder --gdrive-upload --gdrive-parent-id YOUR_FOLDER_ID
```

Print reusable auth values after login:

```bash
./torrent_cli --upload-only --upload-path /path/to/folder --gdrive-upload --gdrive-print-auth
```

Reuse printed auth values directly:

```bash
./torrent_cli --upload-only --upload-path /path/to/folder --gdrive-upload \
  --gdrive-client-id "YOUR_CLIENT_ID" \
  --gdrive-client-secret "YOUR_CLIENT_SECRET" \
  --gdrive-token '{"access_token":"...","refresh_token":"..."}'
```

## CLI Options

```text
-m <magnet-link>                 Download from a magnet link
-t <torrent-file>                Download from a .torrent file
-o <download-dir>                Set the download directory
--upload-only                    Skip download and upload an existing path
--upload-path <file-or-folder>   Path to upload when using --upload-only
--gdrive-upload                  Upload completed files to Google Drive
--gdrive-credentials <file>      Google OAuth credentials JSON file
--gdrive-client-id <id>          Google OAuth client ID
--gdrive-client-secret <secret>  Google OAuth client secret
--gdrive-token <json>            Reusable OAuth token as JSON
--gdrive-parent-id <id>          Destination Google Drive folder ID
--gdrive-print-auth              Print reusable auth values after login
```

## How Resume Works

The app stores torrent session data inside the selected download directory:

```text
<download-dir>/.torrent-cli/
```

Saved `.torrent` metadata is loaded from there when you run the app again without passing a new magnet link or torrent file.

## Notes

- Google Drive uploads require valid OAuth credentials.
- The app is configured to stop seeding after downloads complete.
- Progress is shown with live terminal progress bars for both downloads and uploads.

## Disclaimer

Use this project only for content you have the legal right to download, store, and upload.
