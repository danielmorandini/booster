/*
Copyright (C) 2018 Daniel Morandini

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package packet

import (
	"bufio"
	"fmt"
	"io"
)

type Decoder struct {
	TagSet
	r io.Reader
}

func NewDecoder(r io.Reader, t TagSet) *Decoder {
	d := new(Decoder)
	d.TagSet = t

	if _, ok := r.(io.ByteReader); !ok {
		r = bufio.NewReader(r)
	}
	d.r = r

	return d
}

func (d *Decoder) Decode(packet *Packet) error {
	otr := NewTagReader(d.r, d.PacketOpeningTag) // open tag reader
	ctr := NewTagReader(d.r, d.PacketClosingTag) // close tag reader
	md := NewModuleDecoder(d.r, d.TagSet)        // module decoder

	buf := make([]byte, 4)
	_, err := otr.Read(buf)
	if err != io.EOF {
		return fmt.Errorf("packet: read open tag: %v", err)
	}

	// read modules number
	buf = buf[:2]
	if _, err := io.ReadFull(d.r, buf); err != nil {
		return fmt.Errorf("packet: unable to read modules number: %v", err)
	}

	mn := int(buf[0])<<8 | int(buf[1])
	i := 0

	for {
		i++
		if i > mn {
			buf = buf[:4]
			if _, err := ctr.Read(buf); err != nil {
				if err == io.EOF {
					return nil // we're done
				} else {
					// we counldn't read the closing tags
					return fmt.Errorf("packet: read close tag: %v", err)
				}
			}

			// no error occurred, it means that our buffer is too small
			// for the tag to be fully read, but we know that this is
			// not true.
			return fmt.Errorf("packet: unexpected closing tag: %s", buf)
		}

		// if no closing tag, a module must be present
		m := new(Module)
		if err = md.Decode(m); err != nil {
			return err
		}

		packet.modules[m.id] = m
	}
}

type ModuleDecoder struct {
	TagSet
	r io.Reader
}

func NewModuleDecoder(r io.Reader, t TagSet) *ModuleDecoder {
	d := new(ModuleDecoder)
	d.TagSet = t
	d.r = r

	return d
}

func (d *ModuleDecoder) Decode(m *Module) error {
	r := d.r
	sr := NewTagReader(r, d.Separator)          // separator reader
	otr := NewTagReader(r, d.PayloadOpeningTag) // open tag reader
	ctr := NewTagReader(r, d.PayloadClosingTag) // close tag reader

	// read module id
	buf := make([]byte, 2)
	if _, err := io.ReadFull(r, buf); err != nil {
		return fmt.Errorf("module: unable to read module id: %v", err)
	}
	m.id = string(buf)

	// separator
	if _, err := sr.Read(buf); err != io.EOF {
		return fmt.Errorf("module: read separator: %v", err)
	}
	sr.Flush()

	// read payload size
	if _, err := io.ReadFull(r, buf); err != nil {
		return fmt.Errorf("module: unable to read payload size: %v", err)
	}
	m.size = uint16(buf[0])<<8 | uint16(buf[1])

	// separator
	if _, err := sr.Read(buf); err != io.EOF {
		return fmt.Errorf("module: read separator: %v", err)
	}
	sr.Flush()

	// read encoding type
	buf = buf[:1]
	if _, err := io.ReadFull(r, buf); err != nil {
		return fmt.Errorf("module: unable to read encoding type: %v", err)
	}
	m.encoding = buf[0]

	// payload open tag
	if _, err := otr.Read(buf); err != io.EOF {
		return fmt.Errorf("module: read payload open tag: %v", err)
	}

	buf = make([]byte, m.size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return fmt.Errorf("module: unable to read payload: %v", err)
	}
	m.payload = make([]byte, m.size)
	copy(m.payload, buf)

	// payload close tag
	if _, err := ctr.Read(buf); err != io.EOF {
		return fmt.Errorf("module: read payload close tag: %v", err)
	}

	return nil
}
