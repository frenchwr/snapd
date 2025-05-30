// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2016-2024 Canonical Ltd
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

// Package udev implements integration between snapd, udev and
// snap-confine around tagging character and block devices so that they
// can be accessed by applications.
//
// TODO: Document this better
package udev

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/snapcore/snapd/dirs"
	"github.com/snapcore/snapd/interfaces"
	"github.com/snapcore/snapd/osutil"
	"github.com/snapcore/snapd/sandbox/cgroup"
	"github.com/snapcore/snapd/snap"
	"github.com/snapcore/snapd/systemd"
	"github.com/snapcore/snapd/timings"
)

// Backend is responsible for maintaining udev rules.
type Backend struct {
	preseed     bool
	isContainer bool
}

// Initialize does nothing.
func (b *Backend) Initialize(opts *interfaces.SecurityBackendOptions) error {
	if opts != nil && opts.Preseed {
		b.preseed = true
	}
	// Since snapd 2.68 the udev backend, responsible for writing udev rules to
	// /etc/udev/rules.d and for calling udevadm control --reload-rules, as
	// well as udevadm trigger (with a number of options), is no longer enabled
	// in containers. System administrators retain ability to manage access to
	// real devices at the container level.
	//
	// For context:
	//
	// In Linux, devices are _not_ namespace aware so if a device is accessible
	// in the container (and the container manager has allowed such access)
	// then allow snaps to freely poke the device subject to still-enforced
	// apparmor rules. In "traditional" containers such as docker or podman,
	// where using systemd is unusual and unsupported this doesn't change
	// anything. In system containers such as lxd and incus users may, with or
	// without understanding the consequences, switch the container to
	// privileged mode. In this mode udev does start inside the container, but
	// actively configures devices on the host with undesirable consequences.
	//
	// But we want the backend active when preseeding so preseeded images
	// actually have the files in /var/lib/snapd/cgroup.
	b.isContainer = systemd.IsContainer()
	return nil
}

// Name returns the name of the backend.
func (b *Backend) Name() interfaces.SecuritySystem {
	return interfaces.SecurityUDev
}

// snapRulesFileName returns the path of the snap udev rules file.
func snapRulesFilePath(snapName string) string {
	rulesFileName := fmt.Sprintf("70-%s.rules", snap.SecurityTag(snapName))
	return filepath.Join(dirs.SnapUdevRulesDir, rulesFileName)
}

func snapDeviceCgroupSelfManageFilePath(snapName string) string {
	selfManageFileName := fmt.Sprintf("%s.device", snap.SecurityTag(snapName))
	return filepath.Join(dirs.SnapCgroupPolicyDir, selfManageFileName)
}

// Setup creates udev rules specific to a given snap.
// If any of the rules are changed or removed then udev database is reloaded.
//
// UDev has no concept of a complain mode so confinement options are ignored.
//
// If the method fails it should be re-tried (with a sensible strategy) by the caller.
func (b *Backend) Setup(appSet *interfaces.SnapAppSet, opts interfaces.ConfinementOptions, repo *interfaces.Repository, tm timings.Measurer) error {
	snapName := appSet.InstanceName()
	spec, err := repo.SnapSpecification(b.Name(), appSet, opts)
	if err != nil {
		return fmt.Errorf("cannot obtain udev specification for snap %q: %w", snapName, err)
	}

	udevSpec := spec.(*Specification)
	content := b.deriveContent(udevSpec)
	subsystemTriggers := udevSpec.TriggeredSubsystems()

	if err := os.MkdirAll(dirs.SnapUdevRulesDir, 0755); err != nil {
		return fmt.Errorf("cannot create directory for udev rules: %w", err)
	}
	if err := os.MkdirAll(dirs.SnapCgroupPolicyDir, 0755); err != nil {
		return fmt.Errorf("cannot create directory for cgroup flags: %w", err)
	}

	rulesFilePath := snapRulesFilePath(snapName)
	selfManageDeviceCgroupPath := snapDeviceCgroupSelfManageFilePath(snapName)

	needReload := false
	// content is always empty whenever the snap controls device
	// cgroup
	if len(content) == 0 || udevSpec.ControlsDeviceCgroup() {
		// Make sure that the rules file gets removed when we don't have any
		// content and exists.
		err = os.Remove(rulesFilePath)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		} else if err == nil {
			needReload = true
		}
	} else {
		var rulesBuf bytes.Buffer
		rulesBuf.WriteString("# This file is automatically generated.\n")
		if (opts.DevMode || opts.Classic) && !opts.JailMode {
			rulesBuf.WriteString("# udev tagging/device cgroups disabled with non-strict mode snaps\n")
		}
		for _, snippet := range content {
			if (opts.DevMode || opts.Classic) && !opts.JailMode {
				rulesBuf.WriteRune('#')
				snippet = strings.Replace(snippet, "\n", "\n#", -1)
			}
			rulesBuf.WriteString(snippet)
			rulesBuf.WriteByte('\n')
		}

		rulesFileState := &osutil.MemoryFileState{
			Content: rulesBuf.Bytes(),
			Mode:    0644,
		}

		// EnsureFileState will make sure the file will be only updated when its content
		// has changed and will otherwise return an error which prevents us from reloading
		// udev rules when not needed.
		err = osutil.EnsureFileState(rulesFilePath, rulesFileState)
		if err != nil && !errors.Is(err, osutil.ErrSameState) {
			return err
		} else if !errors.Is(err, osutil.ErrSameState) {
			needReload = true
		}
	}

	// do not trigger a reload when running in preseeding mode (as we're
	// running in a chroot environment and it would most likely fail)
	if needReload && !b.preseed {
		// FIXME: somehow detect the interfaces that were disconnected and set
		// subsystemTriggers appropriately. ATM, it is always going to be empty
		// on disconnect.
		if err := b.reloadRules(subsystemTriggers); err != nil {
			return err
		}
	}

	var deviceBuf bytes.Buffer
	deviceBuf.WriteString("# This file is automatically generated.\n")

	if udevSpec.ControlsDeviceCgroup() {
		// The spec states that the snap can manage its own device
		// cgroup (typically applies to container-like snaps), in which
		// case leave a flag for snap-confine in at a known location.
		deviceBuf.WriteString("# snap is allowed to manage own device cgroup.\n")
		deviceBuf.WriteString("self-managed=true\n")
	}
	if (opts.DevMode || opts.Classic) && !opts.JailMode {
		// Allow devmode
		deviceBuf.WriteString("# snap uses non-strict confinement.\n")
		deviceBuf.WriteString("non-strict=true\n")
	}

	// the file serves as a checkpoint that udev backend was set up
	err = osutil.EnsureFileState(selfManageDeviceCgroupPath, &osutil.MemoryFileState{
		Content: deviceBuf.Bytes(),
		Mode:    0644,
	})
	if err != nil && !errors.Is(err, osutil.ErrSameState) {
		return err
	}
	return nil
}

// Remove removes udev rules specific to a given snap.
// If any of the rules are removed then udev database is reloaded.
//
// This method should be called after removing a snap.
//
// If the method fails it should be re-tried (with a sensible strategy) by the caller.
func (b *Backend) Remove(snapName string) error {
	rulesFilePath := snapRulesFilePath(snapName)
	selfManageDeviceCgroupPath := snapDeviceCgroupSelfManageFilePath(snapName)

	// If file doesn't exist we avoid reloading the udev rules when we return here
	needReload := false
	if err := os.Remove(rulesFilePath); err == nil {
		needReload = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	if err := os.Remove(selfManageDeviceCgroupPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	// FIXME: somehow detect the interfaces that were disconnected and set
	// subsystemTriggers appropriately. ATM, it is always going to be empty
	// on disconnect.
	if needReload {
		return b.reloadRules(nil)
	}
	return nil
}

func (b *Backend) deriveContent(spec *Specification) (content []string) {
	content = append(content, spec.Snippets()...)
	return content
}

func (b *Backend) NewSpecification(appSet *interfaces.SnapAppSet, opts interfaces.ConfinementOptions) interfaces.Specification {
	return &Specification{appSet: appSet}
}

// SandboxFeatures returns the list of features supported by snapd for mediating access to kernel devices.
func (b *Backend) SandboxFeatures() []string {
	commonFeatures := []string{
		"tagging",          /* Tagging dynamically associates new devices with specific snaps */
		"device-filtering", /* Snapd can limit device access for each snap */
	}

	if cgroup.IsUnified() {
		return append(commonFeatures,
			"device-cgroup-v2", /* Snapd creates a device group (v2) for each snap */
		)
	} else {
		return append(commonFeatures,
			"device-cgroup-v1", /* Snapd creates a device group (v1) for each snap */
		)
	}
}
