package codec

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type noopCloser struct {
	io.Reader
}

func (n noopCloser) Close() error {
	return nil
}

func testReaderSuite(t *testing.T, codec, path string, data []byte, expected ...string) {
	t.Run("close before reading", func(t *testing.T) {
		buf := noopCloser{bytes.NewReader(data)}

		ctor, err := GetReader(codec, NewReaderConfig())
		require.NoError(t, err)

		ack := errors.New("default err")

		r, err := ctor(path, buf, func(ctx context.Context, err error) error {
			ack = err
			return nil
		})
		require.NoError(t, err)

		assert.NoError(t, r.Close(context.Background()))
		assert.EqualError(t, ack, "service shutting down")
	})

	t.Run("acks ordered reads", func(t *testing.T) {
		buf := noopCloser{bytes.NewReader(data)}

		ctor, err := GetReader(codec, NewReaderConfig())
		require.NoError(t, err)

		ack := errors.New("default err")

		r, err := ctor(path, buf, func(ctx context.Context, err error) error {
			ack = err
			return nil
		})
		require.NoError(t, err)

		allReads := map[string][]byte{}

		for _, exp := range expected {
			p, ackFn, err := r.Next(context.Background())
			require.NoError(t, err)
			require.NoError(t, ackFn(context.Background(), nil))
			assert.Equal(t, exp, string(p.Get()))
			allReads[string(p.Get())] = p.Get()
		}

		_, _, err = r.Next(context.Background())
		assert.EqualError(t, err, "EOF")

		assert.NoError(t, r.Close(context.Background()))
		assert.NoError(t, ack)

		for k, v := range allReads {
			assert.Equal(t, k, string(v), "Must not corrupt previous reads")
		}
	})

	t.Run("acks unordered reads", func(t *testing.T) {
		buf := noopCloser{bytes.NewReader(data)}

		ctor, err := GetReader(codec, NewReaderConfig())
		require.NoError(t, err)

		ack := errors.New("default err")

		r, err := ctor(path, buf, func(ctx context.Context, err error) error {
			ack = err
			return nil
		})
		require.NoError(t, err)

		allReads := map[string][]byte{}

		var ackFns []ReaderAckFn
		for _, exp := range expected {
			p, ackFn, err := r.Next(context.Background())
			require.NoError(t, err)
			ackFns = append(ackFns, ackFn)
			assert.Equal(t, exp, string(p.Get()))
			allReads[string(p.Get())] = p.Get()
		}

		_, _, err = r.Next(context.Background())
		assert.EqualError(t, err, "EOF")
		assert.NoError(t, r.Close(context.Background()))

		for _, ackFn := range ackFns {
			require.NoError(t, ackFn(context.Background(), nil))
		}

		assert.NoError(t, ack)

		for k, v := range allReads {
			assert.Equal(t, k, string(v), "Must not corrupt previous reads")
		}
	})

	if len(expected) > 0 {
		t.Run("nacks unordered reads", func(t *testing.T) {
			buf := noopCloser{bytes.NewReader(data)}

			ctor, err := GetReader(codec, NewReaderConfig())
			require.NoError(t, err)

			ack := errors.New("default err")
			exp := errors.New("real err")

			r, err := ctor(path, buf, func(ctx context.Context, err error) error {
				ack = err
				return nil
			})
			require.NoError(t, err)

			allReads := map[string][]byte{}

			var ackFns []ReaderAckFn
			for _, exp := range expected {
				p, ackFn, err := r.Next(context.Background())
				require.NoError(t, err)
				ackFns = append(ackFns, ackFn)
				assert.Equal(t, exp, string(p.Get()))
				allReads[string(p.Get())] = p.Get()
			}

			_, _, err = r.Next(context.Background())
			assert.EqualError(t, err, "EOF")
			assert.NoError(t, r.Close(context.Background()))

			for i, ackFn := range ackFns {
				if i == 0 {
					require.NoError(t, ackFn(context.Background(), exp))
				} else {
					require.NoError(t, ackFn(context.Background(), nil))
				}
			}

			assert.EqualError(t, ack, exp.Error())

			for k, v := range allReads {
				assert.Equal(t, k, string(v), "Must not corrupt previous reads")
			}
		})
	}
}

func TestLinesReader(t *testing.T) {
	data := []byte("foo\nbar\nbaz")
	testReaderSuite(t, "lines", "", data, "foo", "bar", "baz")

	data = []byte("")
	testReaderSuite(t, "lines", "", data)
}

func TestCSVReader(t *testing.T) {
	data := []byte("col1,col2,col3\nfoo1,bar1,baz1\nfoo2,bar2,baz2\nfoo3,bar3,baz3")
	testReaderSuite(
		t, "csv", "", data,
		`{"col1":"foo1","col2":"bar1","col3":"baz1"}`,
		`{"col1":"foo2","col2":"bar2","col3":"baz2"}`,
		`{"col1":"foo3","col2":"bar3","col3":"baz3"}`,
	)

	data = []byte("col1,col2,col3")
	testReaderSuite(t, "csv", "", data)
}

func TestAutoReader(t *testing.T) {
	data := []byte("col1,col2,col3\nfoo1,bar1,baz1\nfoo2,bar2,baz2\nfoo3,bar3,baz3")
	testReaderSuite(
		t, "auto", "foo.csv", data,
		`{"col1":"foo1","col2":"bar1","col3":"baz1"}`,
		`{"col1":"foo2","col2":"bar2","col3":"baz2"}`,
		`{"col1":"foo3","col2":"bar3","col3":"baz3"}`,
	)

	data = []byte("col1,col2,col3")
	testReaderSuite(t, "auto", "foo.csv", data)
}

func TestCSVGzipReader(t *testing.T) {
	var gzipBuf bytes.Buffer
	zw := gzip.NewWriter(&gzipBuf)
	zw.Write([]byte("col1,col2,col3\nfoo1,bar1,baz1\nfoo2,bar2,baz2\nfoo3,bar3,baz3"))
	zw.Close()

	testReaderSuite(
		t, "csv-gzip", "", gzipBuf.Bytes(),
		`{"col1":"foo1","col2":"bar1","col3":"baz1"}`,
		`{"col1":"foo2","col2":"bar2","col3":"baz2"}`,
		`{"col1":"foo3","col2":"bar3","col3":"baz3"}`,
	)
}

func TestAllBytesReader(t *testing.T) {
	data := []byte("foo\nbar\nbaz")
	testReaderSuite(t, "all-bytes", "", data, "foo\nbar\nbaz")
}

func TestDelimReader(t *testing.T) {
	data := []byte("fooXbarXbaz")
	testReaderSuite(t, "delim:X", "", data, "foo", "bar", "baz")

	data = []byte("")
	testReaderSuite(t, "delim:X", "", data)
}

func TestTarReader(t *testing.T) {
	input := []string{
		"first document",
		"second document",
		"third document",
	}

	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	for i := range input {
		hdr := &tar.Header{
			Name: fmt.Sprintf("testfile%v", i),
			Mode: 0600,
			Size: int64(len(input[i])),
		}

		err := tw.WriteHeader(hdr)
		require.NoError(t, err)

		_, err = tw.Write([]byte(input[i]))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())

	testReaderSuite(t, "tar", "", tarBuf.Bytes(), input...)
	testReaderSuite(t, "auto", "foo.tar", tarBuf.Bytes(), input...)
}

func TestTarGzipReader(t *testing.T) {
	input := []string{
		"first document",
		"second document",
		"third document",
	}

	var gzipBuf bytes.Buffer

	zw := gzip.NewWriter(&gzipBuf)
	tw := tar.NewWriter(zw)
	for i := range input {
		hdr := &tar.Header{
			Name: fmt.Sprintf("testfile%v", i),
			Mode: 0600,
			Size: int64(len(input[i])),
		}

		err := tw.WriteHeader(hdr)
		require.NoError(t, err)

		_, err = tw.Write([]byte(input[i]))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, zw.Close())

	testReaderSuite(t, "tar-gzip", "", gzipBuf.Bytes(), input...)
	testReaderSuite(t, "auto", "foo.tar.gz", gzipBuf.Bytes(), input...)
	testReaderSuite(t, "auto", "foo.tar.gzip", gzipBuf.Bytes(), input...)
	testReaderSuite(t, "auto", "foo.tgz", gzipBuf.Bytes(), input...)
}
