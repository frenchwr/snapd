// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2020 Canonical Ltd
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3 as
 * published by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package main_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	. "gopkg.in/check.v1"

	main "github.com/snapcore/snapd/cmd/snap-bootstrap"
	"github.com/snapcore/snapd/dirs"
	"github.com/snapcore/snapd/systemd"
	"github.com/snapcore/snapd/testutil"
)

type doSystemdMountSuite struct {
	testutil.BaseTest
}

var _ = Suite(&doSystemdMountSuite{})

func (s *doSystemdMountSuite) SetUpTest(c *C) {
	dirs.SetRootDir(c.MkDir())
	s.AddCleanup(func() { dirs.SetRootDir("") })
}

func (s *doSystemdMountSuite) TestDoSystemdMountUnhappy(c *C) {
	cmd := testutil.MockCommand(c, "systemd-mount", `
echo "mocked error"
exit 1
`)
	defer cmd.Restore()

	err := main.DoSystemdMount("something", "somewhere only we know", nil)
	c.Assert(err, ErrorMatches, "mocked error")
}

func (s *doSystemdMountSuite) TestDoSystemdMount(c *C) {

	testStart := time.Now()

	tt := []struct {
		what             string
		where            string
		opts             *main.SystemdMountOptions
		timeNowTimes     []time.Time
		isMountedReturns []bool
		expErr           string
		comment          string
	}{
		{
			what:             "/dev/sda3",
			where:            "/run/mnt/data",
			timeNowTimes:     []time.Time{testStart, testStart},
			isMountedReturns: []bool{true},
			comment:          "happy default",
		},
		{
			what:  "tmpfs",
			where: "/run/mnt/data",
			opts: &main.SystemdMountOptions{
				Tmpfs: true,
			},
			timeNowTimes:     []time.Time{testStart, testStart},
			isMountedReturns: []bool{true},
			comment:          "happy tmpfs",
		},
		{
			what:  "",
			where: "/run/mnt/data",
			opts: &main.SystemdMountOptions{
				Tmpfs: true,
			},
			timeNowTimes:     []time.Time{testStart, testStart},
			isMountedReturns: []bool{true},
			comment:          "happy tmpfs with empty what argument",
		},
		{
			what:  "tmpfs",
			where: "/run/mnt/data",
			opts: &main.SystemdMountOptions{
				NeedsFsck: true,
			},
			timeNowTimes:     []time.Time{testStart, testStart},
			isMountedReturns: []bool{true},
			comment:          "happy fsck",
		},
		{
			what:  "tmpfs",
			where: "/run/mnt/data",
			opts: &main.SystemdMountOptions{
				Ephemeral: true,
			},
			timeNowTimes:     []time.Time{testStart, testStart},
			isMountedReturns: []bool{true},
			comment:          "happy initramfs ephemeral",
		},
		{
			what:  "tmpfs",
			where: "/run/mnt/data",
			opts: &main.SystemdMountOptions{
				NoWait: true,
			},
			comment: "happy no wait",
		},
		{
			what:             "what",
			where:            "where",
			timeNowTimes:     []time.Time{testStart, testStart, testStart, testStart.Add(2 * time.Minute)},
			isMountedReturns: []bool{false, false},
			expErr:           "timed out after 1m30s waiting for mount what on where",
			comment:          "times out waiting for mount to appear",
		},
		{
			what:  "what",
			where: "where",
			opts: &main.SystemdMountOptions{
				Tmpfs:     true,
				NeedsFsck: true,
			},
			expErr:  "cannot mount \"what\" at \"where\": impossible to fsck a tmpfs",
			comment: "invalid tmpfs + fsck",
		},
		{
			what:  "tmpfs",
			where: "/run/mnt/data",
			opts: &main.SystemdMountOptions{
				NoSuid: true,
			},
			timeNowTimes:     []time.Time{testStart, testStart},
			isMountedReturns: []bool{true},
			comment:          "happy nosuid",
		},
		{
			what:  "tmpfs",
			where: "/run/mnt/data",
			opts: &main.SystemdMountOptions{
				NoDev: true,
			},
			timeNowTimes:     []time.Time{testStart, testStart},
			isMountedReturns: []bool{true},
			comment:          "happy nodev",
		},
		{
			what:  "tmpfs",
			where: "/run/mnt/data",
			opts: &main.SystemdMountOptions{
				NoExec: true,
			},
			timeNowTimes:     []time.Time{testStart, testStart},
			isMountedReturns: []bool{true},
			comment:          "happy noexec",
		},
		{
			what:  "tmpfs",
			where: "/run/mnt/data",
			opts: &main.SystemdMountOptions{
				Bind: true,
			},
			timeNowTimes:     []time.Time{testStart, testStart},
			isMountedReturns: []bool{true},
			comment:          "happy bind",
		},
		{
			what:  "tmpfs",
			where: "/run/mnt/data",
			opts: &main.SystemdMountOptions{
				Umount: true,
			},
			timeNowTimes:     []time.Time{testStart, testStart},
			isMountedReturns: []bool{false},
			comment:          "happy umount",
		},
		{
			what:  "tmpfs",
			where: "/run/mnt/data",
			opts: &main.SystemdMountOptions{
				NoSuid: true,
				Bind:   true,
			},
			timeNowTimes:     []time.Time{testStart, testStart},
			isMountedReturns: []bool{true},
			comment:          "happy nosuid+bind",
		},
		{
			what:  "/run/mnt/data/some.snap",
			where: "/run/mnt/base",
			opts: &main.SystemdMountOptions{
				ReadOnly: true,
			},
			timeNowTimes:     []time.Time{testStart, testStart},
			isMountedReturns: []bool{true},
			comment:          "happy ro",
		},
		{
			// The What argument is ignored for overlay mounts but needs to be a path that exists.
			what:  "/merged",
			where: "/merged",
			opts: &main.SystemdMountOptions{
				FsOpts: &main.OverlayFsOptions{
					LowerDirs: []string{"/lower"},
					UpperDir:  "/upper",
					WorkDir:   "/work",
				},
			},
			timeNowTimes:     []time.Time{testStart, testStart},
			isMountedReturns: []bool{true},
			comment:          "happy overlay mount",
		},
		// TODO: Despite this being a valid path, we forbid the use of backslashes, commas and spaces in all paths.
		// {
		// 	// The What argument is ignored for overlay mounts but needs to be a path that exists.
		// 	what:  "/merged",
		// 	where: "/merged",
		// 	opts: &main.SystemdMountOptions{
		// 		FsOpts: &main.OverlayFsOptions{
		// 			Overlayfs: true,
		// 			LowerDirs: []string{"/lower,"},
		// 			UpperDir:  "/upper",
		// 			WorkDir:   "/work",
		// 		},
		// 	},
		// 	timeNowTimes:     []time.Time{testStart, testStart},
		// 	isMountedReturns: []bool{true},
		// 	comment:          "happy overlay mount with lowerdir path containing a comma",
		// },
		// TODO: Despite this being a valid path, we forbid the use of backslashes, commas and spaces in all paths.
		// {
		// 	// The What argument is ignored for overlay mounts but needs to be a path that exists.
		// 	what:  "/merged",
		// 	where: "/merged",
		// 	opts: &main.SystemdMountOptions{
		// 		FsOpts: &main.OverlayFsOptions{
		// 			LowerDirs: []string{"/lower"},
		// 			UpperDir:  "/upper,",
		// 			WorkDir:   "/work",
		// 		},
		// 	},
		// 	timeNowTimes:     []time.Time{testStart, testStart},
		// 	isMountedReturns: []bool{true},
		// 	comment:          "happy overlay mount with upperdir path containing a comma",
		// },
		// TODO: Despite this being a valid path, we forbid the use of backslashes, commas and spaces in all paths.
		// {
		// 	// The What argument is ignored for overlay mounts but needs to be a path that exists.
		// 	what:  "/merged",
		// 	where: "/merged",
		// 	opts: &main.SystemdMountOptions{
		// 		FsOpts: &main.OverlayFsOptions{
		// 			LowerDirs: []string{"/lower"},
		// 			UpperDir:  "/upper",
		// 			WorkDir:   "/work,",
		// 		},
		// 	},
		// 	timeNowTimes:     []time.Time{testStart, testStart},
		// 	isMountedReturns: []bool{true},
		// 	comment:          "happy overlay mount with workdir path containing a comma",
		// },
		{
			// The What argument is ignored for overlay mounts but needs to be a path that exists.
			what:  "/merged",
			where: "/merged",
			opts: &main.SystemdMountOptions{
				FsOpts: &main.OverlayFsOptions{
					LowerDirs: []string{"/lower1", "/lower2"},
					UpperDir:  "/upper",
					WorkDir:   "/work",
				},
			},
			timeNowTimes:     []time.Time{testStart, testStart},
			isMountedReturns: []bool{true},
			comment:          "happy overlay mount with multiple lowerdirs for overlayfs",
		},
		// TODO: Despite this being a valid path, we also forbid the use of colons in lowerdirs for now.
		// {
		// 	// The What argument is ignored for overlay mounts but needs to be a path that exists.
		// 	what:  "/merged",
		// 	where: "/merged",
		// 	opts: &main.SystemdMountOptions{
		// 		FsOpts: &main.OverlayFsOptions{
		// 			LowerDirs: []string{"/lower1:", "/lower2:"},
		// 			UpperDir:  "/upper",
		// 			WorkDir:   "/work",
		// 		},
		// 	},
		// 	timeNowTimes:     []time.Time{testStart, testStart},
		// 	isMountedReturns: []bool{true},
		// 	comment:          "happy overlay mount with multiple lowerdirs that contain colons",
		// },
		{
			// The What argument is ignored for overlay mounts but needs to be a path that exists.
			what:  "what",
			where: "where",
			opts: &main.SystemdMountOptions{
				FsOpts: &main.OverlayFsOptions{
					UpperDir: "/upper",
					WorkDir:  "/work",
				},
			},
			expErr:  "cannot mount \"what\" at \"where\": missing arguments for overlayfs mount. at least one lowerdir is required",
			comment: "overlayfs mount requested without specifying a lowerdir",
		},
		{
			// The What argument is ignored for overlay mounts but needs to be a path that exists.
			what:  "what",
			where: "where",
			opts: &main.SystemdMountOptions{
				FsOpts: &main.OverlayFsOptions{
					LowerDirs: []string{"/lower1"},
					WorkDir:   "/work",
				},
			},
			expErr:  "cannot mount \"what\" at \"where\": a workdir for an overlayfs mount was specified but upperdir is missing",
			comment: "overlayfs mount requested without specifying an upperdir",
		},
		{
			// The What argument is ignored for overlay mounts but needs to be a path that exists.
			what:  "what",
			where: "where",
			opts: &main.SystemdMountOptions{
				FsOpts: &main.OverlayFsOptions{
					LowerDirs: []string{"/lower1"},
					UpperDir:  "/upper",
				},
			},
			expErr:  "cannot mount \"what\" at \"where\": an upperdir for an overlayfs mount was specified but workdir is missing",
			comment: "overlayfs mount requested without specifying a workdir",
		},
		{
			what:  "what",
			where: "where",
			opts: &main.SystemdMountOptions{
				FsOpts: &main.OverlayFsOptions{
					LowerDirs: []string{"/lower1\\,\" "},
					UpperDir:  "/upper",
					WorkDir:   "/work",
				},
			},
			expErr:  `cannot mount "what" at "where": lowerdir overlayfs mount option contains forbidden characters. "` + regexp.QuoteMeta(`/lower1\\,\" `) + `" contains one of "` + regexp.QuoteMeta(`\\,:\" `) + `"`,
			comment: "disallow use of \\,\" and space in the overlayfs lowerdir mount option",
		},
		{
			what:  "what",
			where: "where",
			opts: &main.SystemdMountOptions{
				FsOpts: &main.OverlayFsOptions{
					LowerDirs: []string{"/lower1:"},
					UpperDir:  "/upper",
					WorkDir:   "/work",
				},
			},
			expErr:  `cannot mount "what" at "where": lowerdir overlayfs mount option contains forbidden characters. "` + regexp.QuoteMeta(`/lower1:`) + `" contains one of "` + regexp.QuoteMeta(`\\,:\" `) + `"`,
			comment: "disallow use of : in the overlayfs lowerdir mount option",
		},
		{
			what:  "what",
			where: "where",
			opts: &main.SystemdMountOptions{
				FsOpts: &main.OverlayFsOptions{
					LowerDirs: []string{"/lower1"},
					UpperDir:  "/upper\\,:\" ",
					WorkDir:   "/work",
				},
			},
			expErr:  `cannot mount "what" at "where": upperdir overlayfs mount option contains forbidden characters. "` + regexp.QuoteMeta(`/upper\\,:\" `) + `" contains one of "` + regexp.QuoteMeta(`\\,:\" `) + `"`,
			comment: "disallow use of \\,:\" and space in the overlayfs upperdir mount option",
		},
		{
			what:  "what",
			where: "where",
			opts: &main.SystemdMountOptions{
				FsOpts: &main.OverlayFsOptions{
					LowerDirs: []string{"/lower1"},
					UpperDir:  "/upper",
					WorkDir:   "/work\\,:\" ",
				},
			},
			expErr:  `cannot mount "what" at "where": workdir overlayfs mount option contains forbidden characters. "` + regexp.QuoteMeta(`/work\\,:\" `) + `" contains one of "` + regexp.QuoteMeta(`\\,:\" `) + `"`,
			comment: "disallow use of \\,:\" and space in the overlayfs workdir mount option",
		},
		{
			what:  "/run/mnt/data/some.snap",
			where: "/run/mnt/base",
			opts: &main.SystemdMountOptions{
				FsOpts: &main.DmVerityOptions{
					HashDevice: "test.verity",
					RootHash:   "00000000000000000000000000000000",
					HashOffset: 4096,
				},
			},
			timeNowTimes:     []time.Time{testStart, testStart},
			isMountedReturns: []bool{true},
			comment:          "happy with attached dm-verity data",
		},
		{
			what:  "/run/mnt/data/some.snap",
			where: "/run/mnt/base",
			opts: &main.SystemdMountOptions{
				FsOpts: &main.DmVerityOptions{
					HashDevice: "test.verity",
					RootHash:   "00000000000000000000000000000000",
				},
			},
			timeNowTimes:     []time.Time{testStart, testStart},
			isMountedReturns: []bool{true},
			comment:          "happy without specifying a verity offset",
		},
		{
			what:  "what",
			where: "where",
			opts: &main.SystemdMountOptions{
				FsOpts: &main.DmVerityOptions{
					HashDevice: "test.verity",
				},
			},
			expErr:  "cannot mount \"what\" at \"where\": mount with dm-verity was requested but a root hash was not specified",
			comment: "verity hash device specified without specifying a verity root hash",
		},
		{
			what:  "what",
			where: "where",
			opts: &main.SystemdMountOptions{
				FsOpts: &main.DmVerityOptions{
					RootHash: "00000000000000000000000000000000",
				},
			},
			expErr:  "cannot mount \"what\" at \"where\": mount with dm-verity was requested but a hash device was not specified",
			comment: "verity root hash specified without specifying a verity hash device",
		},
		{
			what:  "what",
			where: "where",
			opts: &main.SystemdMountOptions{
				FsOpts: &main.DmVerityOptions{
					HashOffset: 4096,
				},
			},
			expErr:  "cannot mount \"what\" at \"where\": mount with dm-verity was requested but a hash device and root hash were not specified",
			comment: "verity hash offset specified without specifying a verity root hash and a verity hash device",
		},
		{
			what:  "what",
			where: "where",
			opts: &main.SystemdMountOptions{
				FsOpts: &main.DmVerityOptions{
					HashDevice: "test.verity\\,:\" ",
					RootHash:   "00000000000000000000000000000000",
				},
			},
			expErr:  `cannot mount "what" at "where": dm-verity hash device path contains forbidden characters. "` + regexp.QuoteMeta(`test.verity\\,:\" `) + `" contains one of "` + regexp.QuoteMeta(`\\,:\" `) + `"`,
			comment: "disallow use of \\,:\": and space in the dm-verity hash device option",
		},
	}

	for _, t := range tt {
		comment := Commentf(t.comment)

		var cleanups []func()

		opts := t.opts
		if opts == nil {
			opts = &main.SystemdMountOptions{}
		}
		dirs.SetRootDir(c.MkDir())
		cleanups = append(cleanups, func() { dirs.SetRootDir("") })

		cmd := testutil.MockCommand(c, "systemd-mount", ``)
		cleanups = append(cleanups, cmd.Restore)

		timeCalls := 0
		restore := main.MockTimeNow(func() time.Time {
			timeCalls++
			c.Assert(timeCalls <= len(t.timeNowTimes), Equals, true, comment)
			if timeCalls > len(t.timeNowTimes) {
				c.Errorf("too many time.Now calls (%d)", timeCalls)
				// we want the test to fail at some point and not run forever, so
				// move time way forward to make it for sure time out
				return testStart.Add(10000 * time.Hour)
			}
			return t.timeNowTimes[timeCalls-1]
		})
		cleanups = append(cleanups, restore)

		cleanups = append(cleanups, func() {
			c.Assert(timeCalls, Equals, len(t.timeNowTimes), comment)
		})

		isMountedCalls := 0
		restore = main.MockOsutilIsMounted(func(where string) (bool, error) {
			isMountedCalls++
			c.Assert(isMountedCalls <= len(t.isMountedReturns), Equals, true, comment)
			if isMountedCalls > len(t.isMountedReturns) {
				e := fmt.Sprintf("too many osutil.IsMounted calls (%d)", isMountedCalls)
				c.Errorf(e)
				// we want the test to fail at some point and not run forever, so
				// move time way forward to make it for sure time out
				return false, errors.New(e)
			}
			return t.isMountedReturns[isMountedCalls-1], nil
		})
		cleanups = append(cleanups, restore)

		cleanups = append(cleanups, func() {
			c.Assert(isMountedCalls, Equals, len(t.isMountedReturns), comment)
		})

		err := main.DoSystemdMount(t.what, t.where, t.opts)
		if t.expErr != "" {
			c.Assert(err, ErrorMatches, t.expErr)
		} else {
			c.Assert(err, IsNil)

			c.Assert(len(cmd.Calls()), Equals, 1)
			call := cmd.Calls()[0]
			args := []string{
				"systemd-mount", t.what, t.where, "--no-pager", "--no-ask-password",
			}
			if opts.Tmpfs {
				args = []string{
					"systemd-mount", "tmpfs", t.where, "--no-pager", "--no-ask-password",
				}
			}
			if opts.Umount {
				args = []string{
					"systemd-mount", t.where, "--umount", "--no-pager", "--no-ask-password",
				}
			}
			c.Assert(call[:len(args)], DeepEquals, args)

			foundTypeTmpfs := false
			foundTypeOverlayfs := false
			foundFsckYes := false
			foundFsckNo := false
			foundNoBlock := false
			foundBeforeInitrdfsTarget := false
			foundNoDev := false
			foundNoSuid := false
			foundNoExec := false
			foundBind := false
			foundReadOnly := false
			foundPrivate := false
			foundOverlayLowerDir := false
			foundOverlayUpperDir := false
			foundOverlayWorkDir := false
			foundVerityHashDevice := false
			foundVerityRootHash := false
			foundVerityHashOffset := false

			for _, arg := range call[len(args):] {
				switch {
				case arg == "--type=tmpfs":
					foundTypeTmpfs = true
				case arg == "--type=overlay":
					foundTypeOverlayfs = true
				case arg == "--fsck=yes":
					foundFsckYes = true
				case arg == "--fsck=no":
					foundFsckNo = true
				case arg == "--no-block":
					foundNoBlock = true
				case arg == "--property=Before=initrd-fs.target":
					foundBeforeInitrdfsTarget = true
				case strings.HasPrefix(arg, "--options="):
					for _, opt := range strings.Split(strings.TrimPrefix(arg, "--options="), ",") {
						switch {
						case opt == "nodev":
							foundNoDev = true
						case opt == "nosuid":
							foundNoSuid = true
						case opt == "noexec":
							foundNoExec = true
						case opt == "bind":
							foundBind = true
						case opt == "ro":
							foundReadOnly = true
						case opt == "private":
							foundPrivate = true
						case strings.HasPrefix(opt, "lowerdir="):
							foundOverlayLowerDir = true
						case strings.HasPrefix(opt, "upperdir="):
							foundOverlayUpperDir = true
						case strings.HasPrefix(opt, "workdir="):
							foundOverlayWorkDir = true
						case strings.HasPrefix(opt, "verity.hashdevice="):
							foundVerityHashDevice = true
						case strings.HasPrefix(opt, "verity.roothash="):
							foundVerityRootHash = true
						case strings.HasPrefix(opt, "verity.hashoffset="):
							foundVerityHashOffset = true
						default:
							c.Logf("Option %q unexpected", opt)
							c.Fail()
						}
					}
				default:
					c.Logf("Argument %q unexpected", arg)
					c.Fail()
				}
			}
			c.Assert(foundTypeTmpfs, Equals, opts.Tmpfs)
			c.Assert(foundFsckYes, Equals, opts.NeedsFsck)
			c.Assert(foundFsckNo, Equals, !opts.NeedsFsck)
			c.Assert(foundNoBlock, Equals, opts.NoWait)
			c.Assert(foundBeforeInitrdfsTarget, Equals, !opts.Ephemeral)
			c.Assert(foundNoDev, Equals, opts.NoDev)
			c.Assert(foundNoSuid, Equals, opts.NoSuid)
			c.Assert(foundNoExec, Equals, opts.NoExec)
			c.Assert(foundBind, Equals, opts.Bind)
			c.Assert(foundReadOnly, Equals, opts.ReadOnly)
			c.Assert(foundPrivate, Equals, opts.Private)

			if opts.FsOpts != nil {
				switch o := opts.FsOpts.(type) {
				case *main.OverlayFsOptions:
					c.Assert(foundTypeOverlayfs, Equals, true)
					c.Assert(foundOverlayLowerDir, Equals, len(o.LowerDirs) > 0)
					c.Assert(foundOverlayUpperDir, Equals, len(o.UpperDir) > 0)
					c.Assert(foundOverlayWorkDir, Equals, len(o.WorkDir) > 0)
				case *main.DmVerityOptions:
					c.Assert(foundVerityHashDevice, Equals, len(o.HashDevice) > 0)
					c.Assert(foundVerityRootHash, Equals, len(o.RootHash) > 0)
					c.Assert(foundVerityHashOffset, Equals, o.HashOffset > 0)
				default:
				}
			}

			// check that the overrides are present if opts.Ephemeral is false,
			// or check the overrides are not present if opts.Ephemeral is true
			for _, initrdUnit := range []string{
				"initrd-fs.target",
				"local-fs.target",
			} {
				mountUnit := systemd.EscapeUnitNamePath(t.where)
				fname := fmt.Sprintf("snap_bootstrap_%s.conf", mountUnit)
				unitFile := filepath.Join(dirs.GlobalRootDir, "/run/systemd/system", initrdUnit+".d", fname)
				if opts.Ephemeral {
					c.Assert(unitFile, testutil.FileAbsent)
				} else {
					c.Assert(unitFile, testutil.FileEquals, fmt.Sprintf(`[Unit]
Wants=%[1]s
`, mountUnit+".mount"))
				}
			}
		}

		for _, r := range cleanups {
			r()
		}
	}
}

type testOpts struct{}

func (d testOpts) AppendOptions(strings []string) ([]string, error) {
	return []string{"test options"}, nil
}

func (s *doSystemdMountSuite) TestDoSystemdMountWrongFsOpts(c *C) {

	opts := &main.SystemdMountOptions{
		FsOpts: testOpts{},
	}

	err := main.DoSystemdMount("what", "where", opts)
	c.Check(err, ErrorMatches, "cannot mount \"what\" at \"where\": invalid options")

}
