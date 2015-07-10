package receiver

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/lomik/go-carbon/logging"
	"github.com/lomik/go-carbon/points"

	"github.com/stretchr/testify/assert"
)

type udpTestCase struct {
	*testing.T
	receiver *Receiver
	conn     net.Conn
	rcvChan  *points.Channel
}

func newUDPTestCase(t *testing.T) *udpTestCase {
	test := &udpTestCase{
		T: t,
	}

	var err error

	test.rcvChan = points.NewChannel(128)
	test.receiver = NewUDP(test.rcvChan)

	settings := test.receiver.Settings()
	settings.ListenAddr = "localhost:0"
	settings.Enabled = true
	assert.NoError(test.T, settings.Apply())

	test.conn, err = net.Dial("udp", test.receiver.Addr().String())
	assert.NoError(test.T, err)

	time.Sleep(5 * time.Millisecond)

	return test
}

func (test *udpTestCase) EnableIncompleteLogging() *udpTestCase {
	settings := test.receiver.Settings()
	settings.LogIncomplete = true
	assert.NoError(test.T, settings.Apply())
	return test
}

func (test *udpTestCase) Finish() {
	if test.conn != nil {
		test.conn.Close()
		test.conn = nil
	}
	if test.receiver != nil {
		test.receiver.Stop()
		test.receiver = nil
	}
}

func (test *udpTestCase) Send(text string) {
	if _, err := test.conn.Write([]byte(text)); err != nil {
		test.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
}

func (test *udpTestCase) Eq(a *points.Points, b *points.Points) {
	if !a.Eq(b) {
		test.Fatalf("%#v != %#v", a, b)
	}
}

func TestUDP1(t *testing.T) {
	test := newUDPTestCase(t)
	defer test.Finish()

	test.Send("hello.world 42.15 1422698155\n")

	select {
	case msg := <-test.rcvChan.OutChan():
		test.Eq(msg, points.OnePoint("hello.world", 42.15, 1422698155))
	default:
		t.Fatalf("Message #0 not received")
	}
}

func TestUDP2(t *testing.T) {
	test := newUDPTestCase(t)
	defer test.Finish()

	test.Send("hello.world 42.15 1422698155\nmetric.name -72.11 1422698155\n")

	select {
	case msg := <-test.rcvChan.OutChan():
		test.Eq(msg, points.OnePoint("hello.world", 42.15, 1422698155))
	default:
		t.Fatalf("Message #0 not received")
	}

	select {
	case msg := <-test.rcvChan.OutChan():
		test.Eq(msg, points.OnePoint("metric.name", -72.11, 1422698155))
	default:
		t.Fatalf("Message #1 not received")
	}
}

func TestChunkedUDP(t *testing.T) {
	test := newUDPTestCase(t)
	defer test.Finish()

	test.Send("hello.world 42.15 1422698155\nmetri")
	test.Send("c.name -72.11 1422698155\n")

	select {
	case msg := <-test.rcvChan.OutChan():
		test.Eq(msg, points.OnePoint("hello.world", 42.15, 1422698155))
	default:
		t.Fatalf("Message #0 not received")
	}

	select {
	case msg := <-test.rcvChan.OutChan():
		test.Eq(msg, points.OnePoint("metric.name", -72.11, 1422698155))
	default:
		t.Fatalf("Message #1 not received")
	}
}

func TestLogIncompleteMessage(t *testing.T) {
	assert := assert.New(t)

	// 3 lines
	logging.Test(func(log *bytes.Buffer) {
		test := newUDPTestCase(t).EnableIncompleteLogging()
		defer test.Finish()

		test.Send("metric1 42 1422698155\nmetric2 43 1422698155\nmetric3 4")
		assert.Contains(log.String(), "metric1 42 1422698155\\n...(21 bytes)...\\nmetric3 4")
	})

	// > 3 lines
	logging.Test(func(log *bytes.Buffer) {
		test := newUDPTestCase(t).EnableIncompleteLogging()
		defer test.Finish()

		test.Send("metric1 42 1422698155\nmetric2 43 1422698155\nmetric3 44 1422698155\nmetric4 45 ")
		assert.Contains(log.String(), "metric1 42 1422698155\\n...(43 bytes)...\\nmetric4 45 ")
	})

	// 2 lines
	logging.Test(func(log *bytes.Buffer) {
		test := newUDPTestCase(t).EnableIncompleteLogging()
		defer test.Finish()

		test.Send("metric1 42 1422698155\nmetric2 43 14226981")
		assert.Contains(log.String(), "metric1 42 1422698155\\nmetric2 43 14226981")
	})

	// single line
	logging.Test(func(log *bytes.Buffer) {
		test := newUDPTestCase(t).EnableIncompleteLogging()
		defer test.Finish()

		test.Send("metric1 42 1422698155")
		assert.Contains(log.String(), "metric1 42 1422698155")
	})
}
