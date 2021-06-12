// armoryctl | https://github.com/f-secure-foundry/armoryctl
//
// USB armory Mk II - hardware control tool
// Copyright (c) F-Secure Corporation
//
// Use of this source code is governed by the license
// that can be found in the LICENSE file.
//
// Links:
//   http://ww1.microchip.com/downloads/en/DeviceDoc/ATECC608A-CryptoAuthentication-Device-Summary-Data-Sheet-DS40001977B.pdf
//   https://github.com/f-secure-foundry/usbarmory/wiki/I%C2%B2C-(Mk-II)

// Package atecc608 supports communication with Microchip ATECC608A and
// ATECC608B secure elements.
package atecc608

import (
	"bytes"
	"fmt"
	"time"
	"encoding/hex"
	"log"

	"github.com/f-secure-foundry/armoryctl/internal"
)

var (
	I2CBus     = 0
	I2CAddress = 0x60
)

const (
	CmdAddress        = 0x03
	CRC16Poly  uint16 = 0x8005
)

// Max command execution time in ms considering a Clock-Divider set to the
// default/recommended value of 0x00.
// (p66, Table 10-5, ATECC608A Full Datasheet)
const CmdMaxExecutionTime = 200 + 50

// Minimum required cmd fields:
//   count (1) + op (1) + param1 (1) + param2 (2) + crc16 (2).
const CmdMinLen = 7

// Minimum required response fields:
//   count (1) + data (1) + crc16 (2).
const ResponseMinLen = 4

// Supported commands.
// (p72, 11. Detailed Command Descriptions, ATECC608A Full Datasheet)
var Cmd = map[string]byte{
	"Read":     0x02,
	"SelfTest": 0x77,
	"SHA256":	0x47,
}

var ShaMode = map[string]byte{
	"SHA_MODE_SHA256_START": 	0,
	"SHA_MODE_SHA256_UPDATE": 	1,
	"SHA_MODE_SHA256_END": 		2,
	"SHA_MODE_SHA256_PUBLIC": 	3,
	"SHA_MODE_HMAC_START": 		4,
	"SHA_MODE_HMAC_UPDATE": 	1,
	"SHA_MODE_HMAC_END": 		5,
	"SHA_MODE_READ_CONTEXT": 	6,
	"SHA_MODE_WRITE_CONTEXT":   	7,
	"SHA_MODE_TARGET_MASK": 	0xC0,
    	"SHA_MODE_TARGET_TEMPKEY":  	0x00,
    	"SHA_MODE_TARGET_MSGDIGBUF":	0x40,
    	"SHA_MODE_TARGET_OUT_ONLY": 	0xC0,
    	// "SHA_RSP_SIZE": 		35,
    	// "SHA_RSP_SIZE_SHORT":      	4,
	// "SHA_RSP_SIZE_LONG":      	35,
}

// Device status/error codes.
// (p64-65, Tab 10-3, ATECC608A Full Datasheet)
var Status = map[byte]string{
	0x00: "successful command execution",
	0x01: "checkmac or verify miscompare",
	0x03: "parse error",
	0x05: "ECC fault",
	0x07: "self test error",
	0x08: "health test error",
	0x0f: "execution error",
	0x11: "after wake, prior to first command",
	0xee: "watchdog about to expire",
	0xff: "CRC or other communications error",
}

// Supported tests and result bit mask.
// (p100, Table 11-43, ATECC608A Full Datasheet)
var Test = map[string]byte{
	"SHA":   	0x20,
	"AES":   	0x10,
	"ECDH":  	0x08,
	"ECDSA Sign": 	0x04,
	"ECDSA Verify": 0x02,
	"RNG DRBG":  	0x01,
}

func crc16(data []byte) []byte {
	var crc uint16

	for i := 0; i < len(data); i++ {
		for shift := uint8(0x01); shift > 0x00; shift <<= 1 {
			// data and crc bits
			var d uint8
			var c uint8

			if uint8(data[i])&uint8(shift) != 0 {
				d = 1
			}

			c = uint8(crc >> 15)
			crc <<= 1

			if d != c {
				crc ^= CRC16Poly
			}
		}
	}

	return []byte{byte(crc & 0xff), byte(crc >> 8)}
}

func verifyResponse(res []byte) (data []byte, err error) {
	// ATECC response packet format:
	//   count [1] | status/error/response data[variable] | crc16 [2]
	//
	// (p63, Table 10-1, ATECC608A Full Datasheet)
	if len(res) < ResponseMinLen {
		err = fmt.Errorf("invalid response, got less than %d bytes", ResponseMinLen)
		return
	}

	size := len(res) - 2

	count := res[0]
	payload := res[:size]
	data = res[1:size]
	crc := res[size:]

	if !bytes.Equal(crc16(payload), crc) {
		err = fmt.Errorf("checksum verification failure")
		return
	}

	if count != 4 {
		// A response with 4 bytes must contain a valid status/error code,
		// otherwise data is being transferred.
		return
	}

	status := data[0]

	if Status[status] == "" {
		err = fmt.Errorf("invalid status/error code: %x", status)
	} else if status != 0x00 && (status <= 0x0f || status == 0xff) {
		err = fmt.Errorf("%s", Status[status])
	}

	return
}

// Wake issues a device wake-up which is always needed before starting a
// new command session.
func Wake() (err error) {
	// Any error at the very first I2CWrite() is silently ignored as
	// the device always returns a "Write Error" here.
	//
	// Writing 0x00 triggers the chip wake-up
	// (p47, 7.1 I/O Conditions, ATECC608A Full Datasheet).
	_ = armoryctl.I2CWrite(I2CBus, 0, 0x01, []byte{0x00})
	log.Printf("[ATSEND Wakeup  addr:%03d %d]\n", 0, 1)

	time.Sleep(CmdMaxExecutionTime * time.Millisecond)

	// It is necessary to read 4 bytes of data to verify that the chip
	// wake-up has been successful.
	res, err := armoryctl.I2CRead(I2CBus, I2CAddress, 0x00, 4)

	if err != nil {
		return
	}

	data, err := verifyResponse(res)

	if err != nil && data[0] != 0x11 {
		err = fmt.Errorf("wake-up failed")
	}

	return
}

// Sleep puts the device in sleep mode which is necessary at the end of each
// command sequence.
func Sleep() {
	_ = armoryctl.I2CWrite(I2CBus, I2CAddress, 0x01, nil)
	log.Printf("[ATSEND  Sleep  addr:%03d %d]\n", I2CAddress, 1)
}

// Idle put the device in idle mode at each command sequence
func Idle() {
	_ = armoryctl.I2CWrite(I2CBus, I2CAddress, 0x02, nil)
	log.Printf("[ATSEND  Idle   addr:%03d %d]\n", I2CAddress, 2)
}

// ExecuteCmd issues an ATECC command conforming to:
//   * p55, Table  9-1, ATECC508A Full Datasheet
//   * p63, Table 10-1, ATECC608A Full Datasheet
//
// The wake flag results in the executed command to be issued individually within a
// Wake() and Sleep() cycle, when the flag is false the caller must take care of
// waking/sleeping according to its desired command sequence.
func ExecuteCmd(opcode byte, param1 [1]byte, param2 [2]byte, data []byte, wake bool) (res []byte, err error) {
	if wake {
		if err = Wake(); err != nil {
			return
		}

		// defer Sleep()
		defer Idle()
	}

	// ATECC cmd packet format:
	//   count [1] | cmd fields [variable] | crc16 [2]
	//
	// ATECC cmd format:
	//   opcode [1] | param1 [1] | param2 [2] | data [variable]
	//
	// (p63, Table 10-1, ATECC608A Full Datasheet)
	cmd := []byte{}
	count := []byte{byte(CmdMinLen + len(data))}
	op := []byte{opcode}

	cmd = append(cmd, count...)
	cmd = append(cmd, op...)
	cmd = append(cmd, param1[:]...)
	cmd = append(cmd, param2[:]...)
	cmd = append(cmd, data...)
	cmd = append(cmd, crc16(cmd)...)

	err = armoryctl.I2CWrite(I2CBus, I2CAddress, CmdAddress, cmd)
	log.Printf("[ATSEND Execute addr:%03d %d Bytes:[%X]]\n", I2CAddress, CmdAddress, cmd)

	if err != nil {
		return
	}

	time.Sleep(CmdMaxExecutionTime * time.Millisecond)

	_ = armoryctl.I2CWrite(I2CBus, I2CAddress, 0x00, []byte{0x00})
	log.Printf("[ATSEND wordadr addr:%03d %d]\n", I2CAddress, 0)

	// The output FIFO is shared among status, error, and command results.
	// The first read command is needed to read how many bytes are present
	// in the output buffer.
	//
	// (p64, 10.3 Status/Error Codes, ATECC608A Full Datasheet)
	resCount, err := armoryctl.I2CRead(I2CBus, I2CAddress, CmdAddress, 1)

	if err != nil {
		return
	}

	// The second read command gets the rest of the response from the
	// output buffer.
	res, err = armoryctl.I2CRead(I2CBus, I2CAddress, CmdAddress, uint(resCount[0]))

	if err != nil {
		return
	}

	return verifyResponse(res)
}

// Execute self test command
func SHA256(sfmt string, msg string) (res string, err error) {
	// param1 0x47: performs SHA256 functions.

	// sha256 init
	data, err := ExecuteCmd(Cmd["SHA256"], [1]byte{ShaMode["SHA_MODE_SHA256_START"]}, [2]byte{0x00, 0x00}, nil, true)

	if err != nil {
	    return
	}

	// sha256 update
	var msg_bytes []byte

	if sfmt == "hex" && len(msg) > 0 {
	    msg_bytes, err = hex.DecodeString(msg)
	    if err != nil {
	        return
	    }
	} else if sfmt == "str" {
	    msg_bytes = []byte(msg)
	}

	block_cnt := len(msg_bytes)/64
	for i := 0; i < block_cnt; i++ {
	    data, err = ExecuteCmd(Cmd["SHA256"], [1]byte{ShaMode["SHA_MODE_SHA256_UPDATE"]}, 
						    [2]byte{0x40,0}, msg_bytes[i*0x40:(i+1)*0x40], true)

	    if err != nil {
		return
	    }
	}

	//sha256 final
	data, err = ExecuteCmd(Cmd["SHA256"], [1]byte{ShaMode["SHA_MODE_SHA256_END"]},
						[2]byte{byte(len(msg_bytes)-block_cnt*64)}, msg_bytes[block_cnt*64:], true)

	if err != nil {
		return
	}
	Sleep()
	return fmt.Sprintf("SHA256 HexDigest: %x", data), nil
}

// Execute self test command
func SelfTest() (res string, err error) {
	// param1 0x3b: performs all available tests.
	data, err := ExecuteCmd(Cmd["SelfTest"], [1]byte{0x3b}, [2]byte{0x00, 0x00}, nil, true)

	if err != nil {
		return
	}

	for k, v := range Test {
		if data[0]&v != 0x00 {
			res += k + ":FAIL "
		} else {
			res += k + ":PASS "
		}
	}
	Sleep()
	return
}

// Read device serial number and software revision
func Info() (res string, err error) {
	// param1 0x80: reads 32 bytes configuration region
	// param2 0x0000: represents the start address
	data, err := ExecuteCmd(Cmd["Read"], [1]byte{0x80}, [2]byte{0x00, 0x00}, nil, true)

	if err != nil {
		return
	}

	// The first 32 bytes in the configuration region will contain:
	// 72 bits - unique serial number: bytes <0:3> and <8:12>
	// 4 bytes - device revision number: bytes <4:7>.
	serial := []byte{}
	serial = append(serial, data[0:4]...)
	serial = append(serial, data[8:13]...)
	revision := data[4:8]
	Sleep()
	return fmt.Sprintf("serial:0x%x revision:0x%x", serial, revision), nil
}
