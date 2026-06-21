# go-fsctl/btrfs

Pure-Go btrfs kernel control: drive btrfs operations directly via `BTRFS_IOC_*`
ioctls on directory file descriptors — no cgo, and no shelling out to the
`btrfs` CLI.

This is the btrfs sibling of [`go-fsctl/zfs`](https://github.com/go-fsctl/zfs)
in the OpenZFS-style `go-fsctl` family: where `go-fsctl/zfs` talks to the
OpenZFS kernel module through `/dev/zfs` and native `nvlist`s, this package
talks to the btrfs kernel module the way `btrfs-progs` does — by opening a
directory on a btrfs mount and issuing struct-based ioctls (magic `'X'` =
`0x94`, `_IOW`/`_IOWR`-encoded).

## Status

Validated against a live Linux 6.12 kernel (arm64) on a `mkfs.btrfs` loopback
mount. The ABI structs and `BTRFS_IOC_*` numbers are derived from the kernel
uapi header `linux/btrfs.h`; see `abi.go`.

## API

```go
import "github.com/go-fsctl/btrfs"

const mnt = "/mnt/bt" // a mounted btrfs filesystem

// Create a subvolume:        BTRFS_IOC_SUBVOL_CREATE
err := btrfs.SubvolCreate(mnt, "sub1")

// Read-only snapshot of it:  BTRFS_IOC_SNAP_CREATE_V2 (flags=BTRFS_SUBVOL_RDONLY)
err = btrfs.SnapshotCreate(mnt+"/sub1", mnt, "snap1", true)

// Toggle the read-only flag: BTRFS_IOC_SUBVOL_GET/SETFLAGS
ro, err := btrfs.IsReadonly(mnt + "/snap1")
err = btrfs.SetReadonly(mnt+"/sub1", true)

// Inspect a subvolume:       BTRFS_IOC_GET_SUBVOL_INFO / BTRFS_IOC_INO_LOOKUP
info, err := btrfs.GetSubvolInfo(mnt + "/sub1")
id, err := btrfs.SubvolID(mnt + "/sub1")

// Force a transaction commit: BTRFS_IOC_SYNC
err = btrfs.Sync(mnt)

// Delete a subvolume/snapshot: BTRFS_IOC_SNAP_DESTROY
err = btrfs.SubvolDelete(mnt, "sub1")

// List all subvolumes:        BTRFS_IOC_TREE_SEARCH(_V2) over the root tree
subs, err := btrfs.ListSubvolumes(mnt) // []Subvolume{ID, ParentID, Name, Path}

// Device management:          BTRFS_IOC_ADD_DEV / RM_DEV_V2 / FS_INFO / DEV_INFO
err = btrfs.DeviceAdd(mnt, "/dev/loop1")
fi, err := btrfs.GetFsInfo(mnt)             // NumDevices, MaxID, FSID, ...
dev, err := btrfs.GetDeviceInfo(mnt, 1)     // Devid, Path, TotalBytes, ...
err = btrfs.DeviceRemove(mnt, "/dev/loop1") // or DeviceRemoveByID(mnt, 2)

// Scrub a device:             BTRFS_IOC_SCRUB / SCRUB_PROGRESS / SCRUB_CANCEL
sp, err := btrfs.ScrubStart(mnt, 1, btrfs.ScrubOptions{}) // synchronous
//          btrfs.ScrubProgressFor(mnt, 1) / btrfs.ScrubCancel(mnt)

// Balance:                    BTRFS_IOC_BALANCE_V2 / BALANCE_PROGRESS / BALANCE_CTL
bp, err := btrfs.BalanceStart(mnt, btrfs.BalanceArgs{}) // full balance, synchronous
//          btrfs.BalanceProgressFor(mnt) / btrfs.BalanceCancel(mnt) / BalancePause(mnt)
// e.g. the `-dusage=50` equivalent (relocate data chunks below 50% full):
//   btrfs.BalanceStart(mnt, btrfs.BalanceArgs{
//       Data: &btrfs.BalanceFilter{Flags: btrfs.BalanceArgsUsage, Usage: 50}})
```

`Available(path)` reports whether `path` is on a mounted btrfs filesystem
(`statfs` + `BTRFS_SUPER_MAGIC`); integration tests use it to skip elsewhere.

## Implemented ioctls

| Operation                | ioctl                       | Struct                          |
| ------------------------ | --------------------------- | ------------------------------- |
| Create subvolume         | `BTRFS_IOC_SUBVOL_CREATE`   | `btrfs_ioctl_vol_args`          |
| Create snapshot (RO/RW)  | `BTRFS_IOC_SNAP_CREATE_V2`  | `btrfs_ioctl_vol_args_v2`       |
| Delete subvolume/snapshot| `BTRFS_IOC_SNAP_DESTROY`    | `btrfs_ioctl_vol_args`          |
| Get subvolume flags      | `BTRFS_IOC_SUBVOL_GETFLAGS` | `__u64`                         |
| Set subvolume flags      | `BTRFS_IOC_SUBVOL_SETFLAGS` | `__u64`                         |
| Subvolume tree id        | `BTRFS_IOC_INO_LOOKUP`      | `btrfs_ioctl_ino_lookup_args`   |
| Subvolume metadata       | `BTRFS_IOC_GET_SUBVOL_INFO` | `btrfs_ioctl_get_subvol_info_args` |
| Force transaction commit | `BTRFS_IOC_SYNC`            | (none)                          |
| List subvolumes          | `BTRFS_IOC_TREE_SEARCH_V2`  | `btrfs_ioctl_search_args_v2` (→ `TREE_SEARCH` fallback) |
| Add device               | `BTRFS_IOC_ADD_DEV`         | `btrfs_ioctl_vol_args`          |
| Remove device            | `BTRFS_IOC_RM_DEV_V2`       | `btrfs_ioctl_vol_args_v2` (→ `RM_DEV` fallback) |
| Filesystem info          | `BTRFS_IOC_FS_INFO`         | `btrfs_ioctl_fs_info_args`      |
| Device info              | `BTRFS_IOC_DEV_INFO`        | `btrfs_ioctl_dev_info_args`     |
| Scrub start / progress   | `BTRFS_IOC_SCRUB` / `SCRUB_PROGRESS` | `btrfs_ioctl_scrub_args` |
| Scrub cancel             | `BTRFS_IOC_SCRUB_CANCEL`    | (none)                          |
| Balance start / progress | `BTRFS_IOC_BALANCE_V2` / `BALANCE_PROGRESS` | `btrfs_ioctl_balance_args` |
| Balance pause / cancel   | `BTRFS_IOC_BALANCE_CTL`     | `int`                           |

### Subvolume listing

`ListSubvolumes` walks the root tree (`BTRFS_ROOT_TREE_OBJECTID`) with the
`TREE_SEARCH` ioctl family, collecting `BTRFS_ROOT_REF` items. Each `ROOT_REF`
key encodes the child subvolume id in its `offset` and the parent subvolume id
in its `objectid`, and its item body (a packed `btrfs_root_ref` followed by the
name) carries the child's name. `Path` is resolved by chaining names up to the
top-level (id 5) subvolume. It prefers `BTRFS_IOC_TREE_SEARCH_V2` (256 KiB
result buffer) and falls back to `BTRFS_IOC_TREE_SEARCH` on kernels without V2.
The root tree is privileged, so listing generally requires root.

### Scrub and balance are synchronous

`BTRFS_IOC_SCRUB` and `BTRFS_IOC_BALANCE_V2` run to completion in the kernel
before the ioctl returns, so `ScrubStart`/`BalanceStart` block and return the
final progress. To poll an in-flight operation, start it from one goroutine and
call `ScrubProgressFor`/`BalanceProgressFor` from another; these return the
kernel's `ENOTCONN` when nothing is running.

## How it works

- **`abi.go`** — the `BTRFS_IOC_*` numbers are recomputed in Go from the
  `_IO`/`_IOR`/`_IOW`/`_IOWR` encoding `(dir<<30)|(size<<16)|(type<<8)|nr` over
  magic `0x94`, rather than hard-coded, so the derivation is self-documenting
  and unit-tested. The C struct layouts (`btrfs_ioctl_vol_args`,
  `..._vol_args_v2`, `..._ino_lookup_args`, `..._get_subvol_info_args`) are
  mirrored as Go structs; the unit tests pin their sizes/offsets to the values
  produced by the kernel uapi header. `golang.org/x/sys/unix` does not define
  these btrfs structs/ioctls at the pinned version, so they live here.
- **`abi_admin.go`** — same treatment for the admin ioctls (listing, device
  management, scrub, balance): numbers recomputed from the encoding, structs
  (`btrfs_ioctl_search_*`, `..._dev_info_args`, `..._fs_info_args`,
  `..._scrub_args`, `..._balance_args`) mirrored and their sizes/offsets pinned
  in `abi_admin_test.go` against a C `offsetof`/`sizeof` probe over the live
  6.12 `linux/btrfs.h`.
- **`btrfs_linux.go`** — opens the target directory, issues the ioctl on its
  fd, and (de)serializes the struct. The `btrfs` CLI is never invoked.
- **`btrfs_admin_linux.go`** — the admin operations: the root-tree walk for
  `ListSubvolumes` and the device/scrub/balance wrappers.
- **`btrfs_other.go`** — non-Linux stub returning `ErrUnsupported`.

## Two read-only flag namespaces

btrfs exposes the subvolume read-only state through two ioctls using *different
bit positions*, which is easy to get wrong:

- `BTRFS_IOC_SUBVOL_GETFLAGS`/`SETFLAGS` use `BTRFS_SUBVOL_RDONLY = (1 << 1)`
  (exported as `SubvolRDONLY`). This is the authoritative runtime state and
  what `IsReadonly` consults.
- `BTRFS_IOC_GET_SUBVOL_INFO` surfaces the raw on-disk `root_item.flags`, where
  read-only is `BTRFS_ROOT_SUBVOL_RDONLY = (1 << 0)` (exported as
  `RootSubvolRDONLY`).

Both were cross-checked against `btrfs property get ... ro` during validation.

## A note on the CLI

The `btrfs` command-line tool is used only in this repo's tests as an
*independent cross-check* of what our ioctls did. The library itself never
spawns it.

## License

BSD-3-Clause. See `LICENSE`.
