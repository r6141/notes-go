package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rjeczalik/notify"
	"github.com/russross/blackfriday/v2"
)

// A single Broker will be created in this program. It is responsible
// for keeping a list of which clients (browsers) are currently attached
// and broadcasting refresh (messages) to those clients.
type Broker struct {

	// Create a map of clients, the keys of the map are the channels
	// over which we can push messages to attached clients.  (The values
	// are just booleans and are meaningless.)
	//
	clients map[chan string]bool

	// Channel into which new clients can be pushed
	//
	newClients chan chan string

	// Channel into which disconnected clients should be pushed
	//
	defunctClients chan chan string

	// Channel into which messages are pushed to be broadcast out
	// to attahed clients.
	//
	messages chan string
}

// This Broker method starts a new goroutine.  It handles
// the addition & removal of clients, as well as the broadcasting
// of messages out to clients that are currently attached.
func (b *Broker) Start() {

	// Start a goroutine
	//
	go func() {

		// Loop endlessly
		//
		for {

			// Block until we receive from one of the
			// three following channels.
			select {

			case s := <-b.newClients:

				// There is a new client attached and we
				// want to start sending them messages.
				b.clients[s] = true
				fmt.Println("Added new client")

			case s := <-b.defunctClients:

				// A client has dettached and we want to
				// stop sending them messages.
				delete(b.clients, s)
				close(s)

				fmt.Println("Removed client")

			case msg := <-b.messages:

				// There is a new message to send.  For each
				// attached client, push the new message
				// into the client's message channel.
				for s := range b.clients {
					s <- msg
				}
				fmt.Printf("Broadcast message to %d clients", len(b.clients))
			}
		}
	}()
}

// This Broker method handles and HTTP request at the "/refresh/" URL.
func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	// Make sure that the writer supports flushing.
	//
	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	// Create a new channel, over which the broker can
	// send this client messages.
	messageChan := make(chan string)

	// Add this client to the map of those that should
	// receive updates
	b.newClients <- messageChan

	// Listen to the closing of the http connection via the CloseNotifier
	notify := w.(http.CloseNotifier).CloseNotify()
	go func() {
		<-notify
		// Remove this client from the map of attached clients
		// when `EventHandler` exits.
		b.defunctClients <- messageChan
		fmt.Println("HTTP connection just closed.")
	}()

	// Set the headers related to event streaming.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Transfer-Encoding", "chunked")

	// Don't close the connection, instead loop endlessly.
	for {

		// Read from our messageChan.
		msg, open := <-messageChan

		if !open {
			// If our messageChan was closed, this means that the client has
			// disconnected.
			break
		}

		// Write to the ResponseWriter, `w`.
		fmt.Fprintf(w, "data: Message: %s\n\n", msg)

		// Flush the response.  This is only possible if
		// the repsonse supports streaming.
		f.Flush()
	}

	// Done.
	fmt.Println("Finished HTTP request at ", r.URL.Path)
}

// This function is called when a request is made to the root URL.
func watchForChanges(dirPath string) bool {
	c := make(chan notify.EventInfo, 1)
	if err := notify.Watch(dirPath+"/...", c, notify.All); err != nil {
		fmt.Println("Error watching directory:", err)
		return false
	}
	defer notify.Stop(c)

	for {
		select {
		case _ = <-c:
			// fmt.Println("File changed:", ei.Path())
			return true
		}
	}
}

// define the root directory to serve
var rootDir string

func main() {
	// Make a new Broker instance
	b := &Broker{
		make(map[chan string]bool),
		make(chan (chan string)),
		make(chan (chan string)),
		make(chan string),
	}

	// Start processing refresh
	b.Start()

	// Make b the HTTP handler for "/refresh/".  It can do
	// this because it has a ServeHTTP method.  That method
	// is called in a separate goroutine for each
	// request to "/refresh/".
	http.Handle("/refresh/", b)

	// get the root directory from command line arguments
	if len(os.Args) > 1 {
		rootDir = os.Args[1]
	} else {
		// if no root directory is provided, use the current working directory
		dir, err := os.Getwd()
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		rootDir = dir
	}

	// start the goroutine to watch for changes
	go func() {
		for {
			if watchForChanges(rootDir) {
				b.messages <- "Files have been changed."
			}
		}
	}()

	// start the server
	http.HandleFunc("/", handler)
	fmt.Println("Server is running and listening on http://localhost:8080")
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

// handler function to handle requests
func handler(w http.ResponseWriter, r *http.Request) {
	// get the path of the request
	path := r.URL.Path
	fmt.Println(path)

	// read the header and footer files
	headerBytes, err := ioutil.ReadFile("header.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	footerBytes, err := ioutil.ReadFile("footer.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// if the path is a directory, show a list of its immediate children
	if isDir(path) {
		entries, err := ioutil.ReadDir(filepath.Join(rootDir, path))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		fmt.Fprintf(w, string(headerBytes))
		fmt.Fprintf(w, "<h1>Directory listing for %s</h1>\n", path)

		// list all immediate children of the directory
		for _, entry := range entries {
			entryName := entry.Name()

			// skip hidden files and directories
			if strings.HasPrefix(entryName, ".") {
				continue
			}

			// generate the URL for the child entry
			entryURL := filepath.Join(path, entryName)
			fmt.Fprintf(w, "<a href=\"%s\">%s</a><br>\n", entryURL, entryName)
		}
		fmt.Fprintf(w, string(footerBytes))
	} else {
		// if the path is a file, show its contents if it's a markdown file, or serve it if it's another type of file
		filePath := filepath.Join(rootDir, path)
		if strings.HasSuffix(path, ".md") {
			mdBytes, err := ioutil.ReadFile(filePath)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			md := string(mdBytes)

			// construct the full HTML page by combining the header, markdown content, and footer
			fullHTML := string(headerBytes) + markdownToHTML(md) + string(footerBytes)

			// write the full HTML page to the response writer
			fmt.Fprint(w, fullHTML)

		} else {
			http.ServeFile(w, r, filePath)
		}
	}
}

// check if a given path is a directory
func isDir(path string) bool {
	fileInfo, err := os.Stat(filepath.Join(rootDir, path))
	if err != nil {
		return false
	}
	return fileInfo.IsDir()
}

func markdownToHTML(md string) string {
	// Convert the markdown to HTML using blackfriday
	renderer := blackfriday.NewHTMLRenderer(blackfriday.HTMLRendererParameters{})
	output := blackfriday.Run([]byte(md), blackfriday.WithRenderer(renderer))

	// Replace display math expressions ($$expr$$) with MathLive script tags
	reDisplay := regexp.MustCompile(`\$\$([^\$]+)\$\$`)
	output = reDisplay.ReplaceAll(output, []byte(`<script type="math/tex; mode=display">$1</script>`))

	// Replace inline math expressions ($expr$) with MathLive script tags
	reInline := regexp.MustCompile(`\$([^\$]+)\$`)
	output = reInline.ReplaceAll(output, []byte(`<script type="math/tex; mode=text">$1</script>`))

	return string(output)
}
