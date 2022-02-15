package library

import (
	"github.com/devfile/registry-support/index/generator/schema"
	gitpkg "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"io"
	"io/ioutil"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"syscall"
	"fmt"
	"github.com/devfile/library/pkg/testingutil/filesystem"
)

// In checks if the value is in the array
func inArray(arr []string, value string) bool {
	for _, item := range arr {
		if item == value {
			return true
		}
	}
	return false
}

func fileExists(filepath string) bool {
	if _, err := os.Stat(filepath); os.IsNotExist(err) {
		return false
	}

	return true
}

func dirExists(dirpath string) error {
	dir, err := os.Stat(dirpath)
	if os.IsNotExist(err){
		return fmt.Errorf("path: %s does not exist: %w",dirpath, err)
	}
	if !dir.IsDir() {
		return fmt.Errorf("%s is not a directory", dirpath)
	}
	return nil
}

// downloadRemoteStack downloads the stack version outside of the registry repo
func downloadRemoteStack(git *schema.Git, path string, verbose bool) (err error) {

	// convert revision to referenceName type, ref name could be a branch or tag
	// if revision is not specified it would be the default branch of the project
	revision := git.Revision
	refName := plumbing.ReferenceName(git.Revision)

	if plumbing.IsHash(revision) {
		// Specifying commit in the reference name is not supported by the go-git library
		// while doing git.PlainClone()
		fmt.Printf("Specifying commit in 'revision' is not yet supported.")
		// overriding revision to empty as we do not support this
		revision = ""
	}

	if revision != "" {
		// lets consider revision to be a branch name first
		refName = plumbing.NewBranchReferenceName(revision)
	}


	cloneOptions := &gitpkg.CloneOptions{
		URL:           git.Url,
		RemoteName:    git.RemoteName,
		ReferenceName: refName,
		SingleBranch:  true,
		// we don't need history for starter projects
		Depth: 1,
	}

	originalPath := ""
	if git.SubDir != "" {
		originalPath = path
		path, err = ioutil.TempDir("", "")
		if err != nil {
			return err
		}
	}

	_, err = gitpkg.PlainClone(path, false, cloneOptions)

	if err != nil {

		// it returns the following error if no matching ref found
		// if we get this error, we are trying again considering revision as tag, only if revision is specified.
		if _, ok := err.(gitpkg.NoMatchingRefSpecError); !ok || revision == "" {
			return err
		}

		// try again to consider revision as tag name
		cloneOptions.ReferenceName = plumbing.NewTagReferenceName(revision)
		// remove if any .git folder downloaded in above try
		_ = os.RemoveAll(filepath.Join(path, ".git"))
		_, err = gitpkg.PlainClone(path, false, cloneOptions)
		if err != nil {
			return err
		}
	}

	// we don't want to download project be a git repo
	err = os.RemoveAll(filepath.Join(path, ".git"))
	if err != nil {
		// we don't need to return (fail) if this happens
		fmt.Printf("Unable to delete .git from cloned devfile repository")
	}

	if git.SubDir != "" {
		err = GitSubDir(path, originalPath,
			git.SubDir)
		if err != nil {
			return err
		}
	}

	return nil

}

// GitSubDir handles subDir for git components using the default filesystem
func GitSubDir(srcPath, destinationPath, subDir string) error {
	return gitSubDir(srcPath, destinationPath, subDir, filesystem.DefaultFs{})
}

// gitSubDir handles subDir for git components
func gitSubDir(srcPath, destinationPath, subDir string, fs filesystem.Filesystem) error {
	go StartSignalWatcher([]os.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, os.Interrupt}, func(_ os.Signal) {
		err := cleanDir(destinationPath, map[string]bool{
			"devfile.yaml": true,
		}, fs)
		if err != nil {
			fmt.Printf("error %v occurred while calling handleInterruptedSubDir", err)
		}
		err = fs.RemoveAll(srcPath)
		if err != nil {
			fmt.Printf("error %v occurred during temp folder clean up", err)
		}
	})

	err := func() error {
		// Open the directory.
		outputDirRead, err := fs.Open(filepath.Join(srcPath, subDir))
		if err != nil {
			return err
		}
		defer func() {
			if err1 := outputDirRead.Close(); err1 != nil {
				fmt.Printf("err occurred while closing temp dir: %v", err1)

			}
		}()
		// Call Readdir to get all files.
		outputDirFiles, err := outputDirRead.Readdir(0)
		if err != nil {
			return err
		}

		// Loop over files.
		for outputIndex := range outputDirFiles {
			outputFileHere := outputDirFiles[outputIndex]

			// Get name of file.
			fileName := outputFileHere.Name()

			oldPath := filepath.Join(srcPath, subDir, fileName)

			if outputFileHere.IsDir() {
				err = copyDirWithFS(oldPath, filepath.Join(destinationPath, fileName), fs)
			} else {
				err = copyFileWithFs(oldPath, filepath.Join(destinationPath, fileName), fs)
			}

			if err != nil {
				return err
			}
		}
		return nil
	}()
	if err != nil {
		return err
	}
	return fs.RemoveAll(srcPath)
}

// copyFileWithFs copies a single file from src to dst
func copyFileWithFs(src, dst string, fs filesystem.Filesystem) error {
	var err error
	var srcinfo os.FileInfo

	srcfd, err := fs.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if e := srcfd.Close(); e != nil {
			fmt.Printf("err occurred while closing file: %v", e)
		}
	}()

	dstfd, err := fs.Create(dst)
	if err != nil {
		return err
	}
	defer func() {
		if e := dstfd.Close(); e != nil {
			fmt.Printf("err occurred while closing file: %v", e)
		}
	}()

	if _, err = io.Copy(dstfd, srcfd); err != nil {
		return err
	}
	if srcinfo, err = fs.Stat(src); err != nil {
		return err
	}
	return fs.Chmod(dst, srcinfo.Mode())
}

// copyDirWithFS copies a whole directory recursively
func copyDirWithFS(src string, dst string, fs filesystem.Filesystem) error {
	var err error
	var fds []os.FileInfo
	var srcinfo os.FileInfo

	if srcinfo, err = fs.Stat(src); err != nil {
		return err
	}

	if err = fs.MkdirAll(dst, srcinfo.Mode()); err != nil {
		return err
	}

	if fds, err = fs.ReadDir(src); err != nil {
		return err
	}
	for _, fd := range fds {
		srcfp := path.Join(src, fd.Name())
		dstfp := path.Join(dst, fd.Name())

		if fd.IsDir() {
			if err = copyDirWithFS(srcfp, dstfp, fs); err != nil {
				return err
			}
		} else {
			if err = copyFileWithFs(srcfp, dstfp, fs); err != nil {
				return err
			}
		}
	}
	return nil
}

// StartSignalWatcher watches for signals and handles the situation before exiting the program
func StartSignalWatcher(watchSignals []os.Signal, handle func(receivedSignal os.Signal)) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, watchSignals...)
	defer signal.Stop(signals)

	receivedSignal := <-signals
	handle(receivedSignal)
	// exit here to stop spinners from rotating
	os.Exit(1)
}

// cleanDir cleans the original folder during events like interrupted copy etc
// it leaves the given files behind for later use
func cleanDir(originalPath string, leaveBehindFiles map[string]bool, fs filesystem.Filesystem) error {
	// Open the directory.
	outputDirRead, err := fs.Open(originalPath)
	if err != nil {
		return err
	}

	// Call Readdir to get all files.
	outputDirFiles, err := outputDirRead.Readdir(0)
	if err != nil {
		return err
	}

	// Loop over files.
	for _, file := range outputDirFiles {
		if value, ok := leaveBehindFiles[file.Name()]; ok && value {
			continue
		}
		err = fs.RemoveAll(filepath.Join(originalPath, file.Name()))
		if err != nil {
			return err
		}
	}
	return err
}