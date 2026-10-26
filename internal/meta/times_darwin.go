package meta

import (
	"encoding/binary"
	"time"

	"golang.org/x/sys/unix"
)

const attrCmnCrtime = 0x00000200 // ATTR_CMN_CRTIME

// setBirthtime выставляет дату создания файла (то, что Finder показывает как «Создан»).
func setBirthtime(path string, t time.Time) error {
	attrs := unix.Attrlist{Bitmapcount: unix.ATTR_BIT_MAP_COUNT, Commonattr: attrCmnCrtime}
	buf := make([]byte, 16) // struct timespec
	binary.LittleEndian.PutUint64(buf[0:], uint64(t.Unix()))
	binary.LittleEndian.PutUint64(buf[8:], uint64(t.Nanosecond()))
	return unix.Setattrlist(path, &attrs, buf, unix.FSOPT_NOFOLLOW)
}
