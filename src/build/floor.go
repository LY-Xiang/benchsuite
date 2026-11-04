package build

import (
	"fmt"
	"os"
	"path/filepath"
)

// The launch floor: the empty program whose start-up cost a timing row subtracts.

// launchFloor compiles the empty program a timing row subtracts, once per process.
//
// The source and flags are identical for every problem, so a sweep of hundreds of
// rows used to pay hundreds of identical compiles. The binary lives in a temp
// directory owned by the Cache and is removed by Close when the run ends; only its
// path is reused, never the measurement -- the floor is timed per problem so both
// numbers of a row describe the same machine.
func (c *Cache) launchFloor() (string, error) {
	c.floorOnce.Do(func() {
		dir, err := os.MkdirTemp("", "bench-floor-*")
		if err != nil {
			c.floorErr = fmt.Errorf("cannot create the launch floor directory: %w", err)
			return
		}
		c.floorDir = dir
		src := filepath.Join(dir, "nop.cpp")
		if err := os.WriteFile(src, []byte(nopSource), 0o644); err != nil {
			c.floorErr = fmt.Errorf("cannot write the launch floor: %w", err)
			return
		}
		bin, err := c.compileTo(src, filepath.Join(dir, "nop"+execSuffix()), KernelFlags)
		if err != nil {
			c.floorErr = fmt.Errorf("launch floor did not build")
			return
		}
		c.floorBin = bin
	})
	return c.floorBin, c.floorErr
}

// closeLaunchFloor removes the shared launch-floor directory. It is safe to call
// when nothing was compiled, and the process is about to exit either way.
func (c *Cache) closeLaunchFloor() {
	if c.floorDir != "" {
		_ = os.RemoveAll(c.floorDir)
		c.floorDir = ""
	}
}
