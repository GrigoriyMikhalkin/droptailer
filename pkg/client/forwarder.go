package client

import (
	"bufio"
	"io"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-systemd/sdjournal"
	timestamp "github.com/golang/protobuf/ptypes/timestamp"
	grpc_retry "github.com/grpc-ecosystem/go-grpc-middleware/retry"
	pb "github.com/metal-pod/droptailer/droptailer"
)

type dropforwarder struct {
	jr       *sdjournal.JournalReader
	dsc      pb.DroptailerClient
	prefixes []string
}

func (d *dropforwarder) run() {
	pr, pw := io.Pipe()
	until := make(chan time.Time)
	go func() {
		if err := d.jr.Follow(until, pw); err != sdjournal.ErrExpired {
			log.Fatalf("Error during follow: %s", err)
		}
		pw.Close()
	}()
	d.writeTo(pr)
}

// writeTo
// actual message will be like
// nftables-metal-dropped: IN=vrf104009 OUT= MAC=12:99:fd:3b:ce:f8:1a:ae:e9:a7:95:50:08:00 SRC=222.73.197.30 DST=212.34.89.87 LEN=40 TOS=0x00 PREC=0x00 TTL=238 ID=46474 PROTO=TCP SPT=59265 DPT=445 WINDOW=1024 RES=0x00 SYN URGP=0
func (d *dropforwarder) writeTo(r io.ReadCloser) {
	br := bufio.NewReader(r)
	for {
		line, _, err := br.ReadLine()
		if err != nil {
			r.Close()
			break
		}
		cr := checkLine(string(line), d.prefixes)
		if cr.skip {
			continue
		}
		fields := parseFields(cr.messageWithoutPrefix)
		de := &pb.Drop{
			Timestamp: &timestamp.Timestamp{Seconds: cr.ts},
			Fields:    fields,
		}
		_, err = d.dsc.Push(
			newCtx(35*time.Second),
			de,
			grpc_retry.WithMax(30),
			grpc_retry.WithPerRetryTimeout(1*time.Second))
		if err != nil {
			log.Printf("unable to send dropentry:%v", err)
		}
	}
}

type checkResult struct {
	skip                 bool
	messageWithoutPrefix string
	ts                   int64
}

func checkLine(l string, prefixes []string) checkResult {
	parts := strings.Split(string(l), "@")
	if len(parts) < 2 {
		return checkResult{skip: true}
	}
	ts, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		log.Printf("unable to parse timestamp:%v", err)
		return checkResult{skip: true}
	}
	msg := parts[1]
	for _, prefix := range prefixes {
		if strings.HasPrefix(msg, prefix) {
			m := strings.TrimPrefix(msg, prefix)
			return checkResult{skip: false, messageWithoutPrefix: m, ts: ts}
		}
	}
	return checkResult{skip: true}
}

func parseFields(msg string) map[string]string {
	fields := make(map[string]string)
	parts := strings.Fields(msg)
	for _, part := range parts {
		fieldParts := strings.Split(part, "=")
		if len(fieldParts) == 0 {
			continue
		}
		key := fieldParts[0]
		fields[key] = ""
		if len(fieldParts) == 1 {
			continue
		}
		fields[key] = fieldParts[1]
	}
	return fields
}