package main

import (
	"fmt"
	"hash"
	"os"

	"crypto/sha512"
	"io/ioutil"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
)

// walkRootMaildir expects the path to the monitored
// node's Maildir directory containing one folder per
// user in the system. It creates initial structures
// for per user file system walks.
func walkRootMaildir(maildirRootPath string) ([]*UserMaildir, error) {

	i := 0
	userMaildirs := make([]*UserMaildir, 0, 30)

	// Retrieve all file system elements in maildirRootPath.
	files, _ := ioutil.ReadDir(maildirRootPath)
	for _, f := range files {

		if f.IsDir() {

			// Create new file system watcher for this user.
			w, err := fsnotify.NewWatcher()
			if err != nil {
				return nil, err
			}

			// Create new item for this user.
			userMaildirs = append(userMaildirs, &UserMaildir{
				userPath:     filepath.Join(maildirRootPath, f.Name()),
				watcher:      w,
				walkTrigger:  make(chan struct{}),
				watchTrigger: make(chan struct{}),
				done:         make(chan struct{}),
			})

			i++
		}
	}

	return userMaildirs, nil
}

// walk deterministically builds the file system
// representation of MaildirItems for all folders
// and files below the user's Maildir path when
// triggered. It also calculates initial metrics.
func (m *UserMaildir) walk(logger log.Logger, metrics *Metrics, done chan struct{}) {

	// Close channel on function exit.
	defer close(m.walkTrigger)

	for {

		select {

		case <-m.walkTrigger:

			var numElems float64 = 0.0
			var numFolders float64 = 0.0
			var numFiles float64 = 0.0
			var numSize float64 = 0.0
			var shaHash hash.Hash = sha512.New()

			err := filepath.Walk(m.userPath, func(path string, info os.FileInfo, err error) error {

				if err != nil {
					return err
				}

				// Do not include the user's Maildir root path,
				// but add it to this user's file system watcher.
				if path == m.userPath {

					absPath, err := filepath.Abs(path)
					if err != nil {
						return err
					}

					err = m.watcher.Add(absPath)
					if err != nil {
						return err
					}

					return nil
				}

				// Maildirs only consist of folders and files,
				// thus ignore all other elements.
				if !(info.IsDir() || info.Mode().IsRegular()) {
					return nil
				}

				if info.IsDir() {

					numFolders++

					absPath, err := filepath.Abs(path)
					if err != nil {
						return err
					}

					// Add this sub directory to this user's watcher.
					err = m.watcher.Add(absPath)
					if err != nil {
						return err
					}
				} else if info.Mode().IsRegular() {
					numFiles++
				}

				numElems++
				numSize += float64(info.Size())

				// Add element to checksum calculation.
				shaHash.Write([]byte(path))

				return nil
			})
			if err != nil {
				level.Error(logger).Log(
					"msg", "error while walking user Maildir",
					"err", err,
				)
				return
			}

			// Set updated metrics in supplied struct.
			metrics.elements.With(prometheus.Labels{"user": m.userPath}).Set(numElems)
			metrics.folders.With(prometheus.Labels{"user": m.userPath}).Set(numFolders)
			metrics.files.With(prometheus.Labels{"user": m.userPath}).Set(numFiles)

			// Include the calculated SHA512 checksum for this Maildir.
			metrics.size.With(prometheus.Labels{
				"user":   m.userPath,
				"sha512": fmt.Sprintf("%x", shaHash.Sum(nil)),
			}).Set(numSize)

			// Signal walk completion downstream.
			done <- struct{}{}

		case <-m.done:
			level.Debug(logger).Log("msg", fmt.Sprintf("done walking Maildir for %s", m.userPath))
			return
		}
	}
}