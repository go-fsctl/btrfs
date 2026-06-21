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

## How it works

- **`abi.go`** — the `BTRFS_IOC_*` numbers are recomputed in Go from the
  `_IO`/`_IOR`/`_IOW`/`_IOWR` encoding `(dir<<30)|(size<<16)|(type<<8)|nr` over
  magic `0x94`, rather than hard-coded, so the derivation is self-documenting
  and unit-tested. The C struct layouts (`btrfs_ioctl_vol_args`,
  `..._vol_args_v2`, `..._ino_lookup_args`, `..._get_subvol_info_args`) are
  mirrored as Go structs; the unit tests pin their sizes/offsets to the values
  produced by the kernel uapi header. `golang.org/x/sys/unix` does not define
  these btrfs structs/ioctls at the pinned version, so they live here.
- **`btrfs_linux.go`** — opens the target directory, issues the ioctl on its
  fd, and (de)serializes the struct. The `btrfs` CLI is never invoked.
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
