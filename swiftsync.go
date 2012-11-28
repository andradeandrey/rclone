// Sync files and directories to and from swift
// 
// Nick Craig-Wood <nick@craig-wood.com>
package main

import (
	"flag"
	"fmt"
	"github.com/ncw/swift"
	"log"
	"os"
	"path/filepath"
	"runtime/pprof"
)

// Globals
var (
	// Flags
	//fileSize      = flag.Int64("s", 1E9, "Size of the check files")
	cpuprofile = flag.String("cpuprofile", "", "Write cpu profile to file")
	//duration      = flag.Duration("duration", time.Hour*24, "Duration to run test")
	//statsInterval = flag.Duration("stats", time.Minute*1, "Interval to print stats")
	//logfile       = flag.String("logfile", "stressdisk.log", "File to write log to set to empty to ignore")

	snet    = flag.Bool("snet", false, "Use internal service network") // FIXME not implemented
	verbose = flag.Bool("verbose", false, "Print lots more stuff")
	quiet   = flag.Bool("quiet", false, "Print as little stuff as possible")
	// FIXME make these part of swift so we get a standard set of flags?
	authUrl  = flag.String("auth", os.Getenv("ST_AUTH"), "Auth URL for server. Defaults to environment var ST_AUTH.")
	userName = flag.String("user", os.Getenv("ST_USER"), "User name. Defaults to environment var ST_USER.")
	apiKey   = flag.String("key", os.Getenv("ST_KEY"), "API key (password). Defaults to environment var ST_KEY.")
)

type FsObject struct {
	rel  string
	path string
	info os.FileInfo
}

type FsObjects map[string]FsObject

// Checks to see if an object has changed or not by looking at its size and mtime
//
// This is the heuristic rsync uses when not using --checksum
func (fs *FsObject) changed(c *swift.Connection, container string) bool {
	obj, h, err := c.Object(container, fs.rel)
	if err != nil {
		log.Printf("Failed to read info %s: %s", fs.path, err)
		return true
	}
	if obj.Bytes != fs.info.Size() {
		log.Printf("Sizes differ %s", fs.path)
		return true
	}
	m := h.ObjectMetadata()
	t, err := m.GetModTime()
	if err != nil {
		log.Printf("Failed to read mtime %s: %s", fs.path, err)
		return true
	}
	if !t.Equal(fs.info.ModTime()) {
		log.Printf("mtimes differ: %s", fs.path)
		return true
	}
	return false
}

// Puts the FsObject into the container
func (fs *FsObject) put(c *swift.Connection, container string) {
	mode := fs.info.Mode()
	if mode&(os.ModeSymlink|os.ModeNamedPipe|os.ModeSocket|os.ModeDevice) != 0 {
		log.Printf("Can't transfer non file/directory %s", fs.path)
	} else if mode&os.ModeDir != 0 {
		// Debug?
		log.Printf("FIXME Skipping directory %s", fs.path)
	} else {
		// Check to see if changed or not
		if !fs.changed(c, container) {
			log.Printf("Unchanged skipping %s", fs.path)
			return
		}
		// FIXME content type
		in, err := os.Open(fs.path)
		if err != nil {
			log.Printf("Failed to open %s: %s", fs.path, err)
			return
		}
		defer in.Close()
		m := swift.Metadata{}
		m.SetModTime(fs.info.ModTime())
		log.Println(m.ObjectHeaders())
		_, err = c.ObjectPut(container, fs.rel, in, true, "", "", m.ObjectHeaders())
		if err != nil {
			log.Printf("Failed to upload %s: %s", fs.path, err)
			return
		}
		log.Printf("Uploaded %s", fs.path)
	}

}

// Walk the path
//
// FIXME ignore symlinks?
// FIXME what about hardlinks / etc
func walk(root string) FsObjects {
	files := make(FsObjects)
	err := filepath.Walk(root, func(path string, f os.FileInfo, err error) error {
		if err != nil {
			log.Printf("Failed to open directory: %s: %s", path, err)
		} else {
			info, err := os.Stat(path)
			if err != nil {
				log.Printf("Failed to stat %s: %s", path, err)
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				log.Printf("Failed to get relative path %s: %s", path, err)
				return nil
			}
			if rel == "." {
				rel = ""
			}
			files[rel] = FsObject{rel: rel, path: path, info: info}
		}
		return nil
	})
	if err != nil {
		log.Printf("Failed to open directory: %s: %s", root, err)
	}
	return files
}

// syntaxError prints the syntax
func syntaxError() {
	fmt.Fprintf(os.Stderr, `Sync files and directores to and from swift

FIXME

Full options:
`)
	flag.PrintDefaults()
}

// Exit with the message
func fatal(message string, args ...interface{}) {
	syntaxError()
	fmt.Fprintf(os.Stderr, message, args...)
	os.Exit(1)
}

// checkArgs checks there are enough arguments and prints a message if not
func checkArgs(args []string, n int, message string) {
	if len(args) != n {
		syntaxError()
		fmt.Fprintf(os.Stderr, "%d arguments required: %s\n", n, message)
		os.Exit(1)
	}
}

// uploads a file into a container
func upload(c *swift.Connection, root, container string) {
	files := walk(root)
	for _, fs := range files {
		fs.put(c, container)
	}
}

// Lists the containers
func listContainers(c *swift.Connection) {
	containers, err := c.Containers(nil)
	if err != nil {
		log.Fatalf("Couldn't list containers: %s", err)
	}
	for _, container := range containers {
		fmt.Printf("%9d %12d %s\n", container.Count, container.Bytes, container.Name)
	}
}

// Lists files in a container
func list(c *swift.Connection, container string) {
	//objects, err := c.Objects(container, &swift.ObjectsOpts{Prefix: "", Delimiter: '/'})
	objects, err := c.Objects(container, nil)
	if err != nil {
		log.Fatalf("Couldn't read container %q: %s", container, err)
	}
	for _, object := range objects {
		if object.PseudoDirectory {
			fmt.Printf("%9s %19s %s\n", "Directory", "-", object.Name)
		} else {
			fmt.Printf("%9d %19s %s\n", object.Bytes, object.LastModified.Format("2006-01-02 15:04:05"), object.Name)
		}
	}
}

// Makes a container
func mkdir(c *swift.Connection, container string) {
	err := c.ContainerCreate(container, nil)
	if err != nil {
		log.Fatalf("Couldn't create container %q: %s", container, err)
	}
}

// Removes a container
func rmdir(c *swift.Connection, container string) {
	err := c.ContainerDelete(container)
	if err != nil {
		log.Fatalf("Couldn't delete container %q: %s", container, err)
	}
}

func main() {
	flag.Usage = syntaxError
	flag.Parse()
	args := flag.Args()
	//runtime.GOMAXPROCS(3)

	// Setup profiling if desired
	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal(err)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	fmt.Println(args)
	if len(args) < 1 {
		fatal("No command supplied\n")
	}

	if *userName == "" {
		log.Fatal("Need -user or environmental variable ST_USER")
	}
	if *apiKey == "" {
		log.Fatal("Need -key or environmental variable ST_KEY")
	}
	if *authUrl == "" {
		log.Fatal("Need -auth or environmental variable ST_AUTH")
	}
	c := &swift.Connection{
		UserName: *userName,
		ApiKey:   *apiKey,
		AuthUrl:  *authUrl,
	}
	err := c.Authenticate()
	if err != nil {
		log.Fatal("Failed to authenticate", err)
	}

	command := args[0]
	args = args[1:]

	switch command {
	case "up", "upload":
		checkArgs(args, 2, "Need directory to read from and container to write to")
		upload(c, args[0], args[1])
	case "list", "ls":
		if len(args) == 0 {
			listContainers(c)
		} else {
			checkArgs(args, 1, "Need container to list")
			list(c, args[0])
		}
	case "mkdir":
		checkArgs(args, 1, "Need container to make")
		mkdir(c, args[0])
	case "rmdir":
		checkArgs(args, 1, "Need container to delte")
		rmdir(c, args[0])
	default:
		log.Fatalf("Unknown command %q", command)
	}

}