package copy

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	// tmpPermissionForDirectory makes the destination directory writable,
	// so that stuff can be copied recursively even if any original directory is NOT writable.
	// See https://github.com/otiai10/copy/pull/9 for more information.
	tmpPermissionForDirectory = os.FileMode(0755)
)

var (
	// If set to true, unsupported file types will not be copied, and no error will be generated
	IgnoreUnsupportedFileTypes = false
	// If set to true, the permission of the source files and folder will be preserved in the destination
	PreservePermissions = false
	// If set to true, the owner of the source files and folder will be preserved in the destination
	PreserveOwner = false
	// If set to true, the access and modifications times of the source files and folder will be preserved in the destination
	PreserveTime = false
)

type FileCopyHandler func(src, dest string, info os.FileInfo) error

var FileTypeCopyHandlers = map[os.FileMode]FileCopyHandler{
	os.ModeSymlink: lcopy,
}

// Copy copies src to dest, doesn't matter if src is a directory or a file
func Copy(src, dest string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}

	err = copy(src, dest, info)
	if err != nil {
		// If we encountered an unsupported file type, exit only if we don't ignore them
		if _, ok := err.(*UnsupportedFileTypeError); ok {
			if !IgnoreUnsupportedFileTypes {
				return err
			}
		} else {
			return err
		}
	}

	return nil
}

// copy dispatches copy-funcs according to the mode.
// Because this "copy" could be called recursively,
// "info" MUST be given here, NOT nil.
func copy(src, dest string, info os.FileInfo) error {

	var err error
	if info.Mode().IsRegular() {
		err = fcopy(src, dest, info)
	} else if info.IsDir() {
		err = dcopy(src, dest, info)
	} else {
		for fileType, handler := range FileTypeCopyHandlers {
			if info.Mode()&fileType != 0 {
				err = handler(src, dest, info)
				break
			}
		}

		err = &UnsupportedFileTypeError{
			mode: info.Mode(),
			path: src,
		}
	}

	if err != nil {
		return err
	}

	var errs []error
	if PreservePermissions {
		err = os.Chmod(dest, info.Mode().Perm())
		if err != nil {
			err = fmt.Errorf("could not restore permissions '%s' for file %s: %w", info.Mode().Perm().String(), dest, err)
			errs = append(errs, err)
		}
	}

	var stat *syscall.Stat_t
	if PreserveOwner || PreserveTime {
		stat, _ = info.Sys().(*syscall.Stat_t)
	}

	if PreserveOwner {
		if stat != nil {
			err = os.Lchown(dest, int(stat.Uid), int(stat.Gid))
			if err != nil {
				err = fmt.Errorf("could not restore owner %d:%d for file %s: %w", stat.Uid, stat.Gid, dest, err)
			}
		} else {
			err = fmt.Errorf("could not restore owner for file %s: %w", dest, err)
		}

		if err != nil {
			errs = append(errs, err)
		}
	}

	if PreserveTime {
		if stat != nil {
			atime := time.Unix(int64(stat.Atim.Sec), int64(stat.Atim.Nsec))
			mtime := info.ModTime()
			err = os.Chtimes(dest, atime, mtime)
			if err != nil {
				err = fmt.Errorf("could not restore timestamp '%s' for file %s: %w", info.ModTime().String(), dest, err)
			}

		} else {
			err = fmt.Errorf("could not restore timestamp for file %s: %w", dest, err)
		}

		if err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return &FileCopyTasksError{
			path:   dest,
			errors: errs,
		}
	} else {
		return nil
	}
}

// fcopy is for just a file,
// with considering existence of parent directory
// and file permission.
func fcopy(src, dest string, info os.FileInfo) error {

	if err := os.MkdirAll(filepath.Dir(dest), os.ModePerm); err != nil {
		return err
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	if err = os.Chmod(f.Name(), info.Mode()); err != nil {
		return err
	}

	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()

	_, err = io.Copy(f, s)
	return err
}

// dcopy is for a directory,
// with scanning contents inside the directory
// and pass everything to "copy" recursively.
func dcopy(srcdir, destdir string, info os.FileInfo) error {

	originalMode := info.Mode()

	// Make dest dir with 0755 so that everything writable.
	if err := os.MkdirAll(destdir, tmpPermissionForDirectory); err != nil {
		return err
	}
	// Recover dir mode with original one.
	defer os.Chmod(destdir, originalMode)

	contents, err := ioutil.ReadDir(srcdir)
	if err != nil {
		return err
	}

	for _, content := range contents {
		cs, cd := filepath.Join(srcdir, content.Name()), filepath.Join(destdir, content.Name())
		if err := copy(cs, cd, content); err != nil {

			// If we encountered an unsupported file type, exit only if we don't ignore them
			if _, ok := err.(*UnsupportedFileTypeError); ok {
				if !IgnoreUnsupportedFileTypes {
					return err
				}
			} else {
				// If any error, exit immediately
				return err
			}
		}
	}

	return nil
}

// lcopy is for a symlink,
// with just creating a new symlink by replicating src symlink.
func lcopy(src, dest string, info os.FileInfo) error {
	src, err := os.Readlink(src)
	if err != nil {
		return err
	}
	return os.Symlink(src, dest)
}

type UnsupportedFileTypeError struct {
	msg  string
	mode os.FileMode
	path string
}

func (e *UnsupportedFileTypeError) Error() string {
	return fmt.Sprintf("unsupported mode '%s' for file %s", e.mode.String(), e.path)
}

type FileCopyTasksError struct {
	path   string
	errors []error
}

func (e *FileCopyTasksError) Error() string {
	return fmt.Sprintf("some tasks after the copy of file %s could not be achieved", e.path)
}
