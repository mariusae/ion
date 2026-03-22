package text

import (
	"fmt"
	"io"
	"os"
)

const (
	blockIncr = 256
	// MaxBlock matches sam's Maxblock constant in runes.
	MaxBlock = 8 * 1024
)

type block struct {
	addr int64
	n    int
}

// Disk is the disk-backed block allocator used by Buffer.
type Disk struct {
	file     *os.File
	fileName string
	addr     int64
	free     [MaxBlock/blockIncr + 1][]*block
}

// NewDisk creates a new temporary backing store.
func NewDisk() (*Disk, error) {
	f, err := os.CreateTemp("", "ion-sam-*")
	if err != nil {
		return nil, fmt.Errorf("create temp disk: %w", err)
	}
	return &Disk{
		file:     f,
		fileName: f.Name(),
	}, nil
}

// Close closes and removes the temporary backing file.
func (d *Disk) Close() error {
	if d == nil || d.file == nil {
		return nil
	}

	name := d.fileName
	errClose := d.file.Close()
	d.file = nil
	d.fileName = ""
	errRemove := os.Remove(name)
	if errClose != nil {
		return errClose
	}
	if errRemove != nil && !os.IsNotExist(errRemove) {
		return errRemove
	}
	return nil
}

func (d *Disk) newBlock(n int) (*block, error) {
	size, bucket, err := blockSize(n)
	if err != nil {
		return nil, err
	}
	freeList := d.free[bucket]
	last := len(freeList) - 1
	if last >= 0 {
		b := freeList[last]
		d.free[bucket] = freeList[:last]
		b.n = n
		return b, nil
	}

	addr := d.addr
	if addr+size < addr {
		return nil, fmt.Errorf("temp file overflow")
	}
	d.addr += size
	return &block{addr: addr, n: n}, nil
}

func (d *Disk) release(b *block) error {
	if b == nil {
		return nil
	}
	_, bucket, err := blockSize(b.n)
	if err != nil {
		return err
	}
	d.free[bucket] = append(d.free[bucket], b)
	return nil
}

func (d *Disk) write(bp **block, r []rune, n int) error {
	b := *bp
	if b == nil {
		return fmt.Errorf("nil block")
	}

	size, _, err := blockSize(b.n)
	if err != nil {
		return err
	}
	nsize, _, err := blockSize(n)
	if err != nil {
		return err
	}
	if size != nsize {
		if err := d.release(b); err != nil {
			return err
		}
		b, err = d.newBlock(n)
		if err != nil {
			return err
		}
		*bp = b
	}

	buf := runesToBytes(r[:n])
	if _, err := d.file.WriteAt(buf, b.addr); err != nil {
		return fmt.Errorf("write temp block: %w", err)
	}
	b.n = n
	return nil
}

func (d *Disk) read(b *block, r []rune, n int) error {
	if b == nil {
		return fmt.Errorf("nil block")
	}
	if n > b.n {
		return fmt.Errorf("diskread beyond block size")
	}
	if _, _, err := blockSize(b.n); err != nil {
		return err
	}

	buf := make([]byte, n*4)
	if _, err := io.ReadFull(io.NewSectionReader(d.file, b.addr, int64(len(buf))), buf); err != nil {
		return fmt.Errorf("read temp block: %w", err)
	}
	bytesToRunes(buf, r[:n])
	return nil
}

func blockSize(n int) (sizeBytes int64, bucket int, err error) {
	if n < 0 || n > MaxBlock {
		return 0, 0, fmt.Errorf("block size out of range: %d", n)
	}
	size := n
	if rem := size & (blockIncr - 1); rem != 0 {
		size += blockIncr - rem
	}
	return int64(size * 4), size / blockIncr, nil
}

func runesToBytes(r []rune) []byte {
	buf := make([]byte, len(r)*4)
	for i, rn := range r {
		v := uint32(rn)
		base := i * 4
		buf[base+0] = byte(v)
		buf[base+1] = byte(v >> 8)
		buf[base+2] = byte(v >> 16)
		buf[base+3] = byte(v >> 24)
	}
	return buf
}

func bytesToRunes(src []byte, dst []rune) {
	for i := range dst {
		base := i * 4
		v := uint32(src[base+0]) |
			uint32(src[base+1])<<8 |
			uint32(src[base+2])<<16 |
			uint32(src[base+3])<<24
		dst[i] = rune(v)
	}
}
