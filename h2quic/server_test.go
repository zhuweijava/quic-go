package h2quic

import (
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"github.com/lucas-clemente/quic-go/protocol"
	"github.com/lucas-clemente/quic-go/testdata"
	"github.com/lucas-clemente/quic-go/utils"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

type mockSession struct {
	closed     bool
	dataStream *mockStream
}

func (s *mockSession) GetOrOpenStream(id protocol.StreamID) (utils.Stream, error) {
	return s.dataStream, nil
}

func (s *mockSession) Close(error) error { s.closed = true; return nil }

var _ = Describe("H2 server", func() {
	const port = "4826"
	const addr = "127.0.0.1:" + port

	var (
		s          *Server
		session    *mockSession
		dataStream *mockStream
	)

	BeforeEach(func() {
		s = &Server{
			Server: &http.Server{
				TLSConfig: testdata.GetTLSConfig(),
			},
		}
		dataStream = &mockStream{}
		session = &mockSession{dataStream: dataStream}
	})

	Context("handling requests", func() {
		var (
			h2framer     *http2.Framer
			hpackDecoder *hpack.Decoder
			headerStream *mockStream
		)

		BeforeEach(func() {
			headerStream = &mockStream{}
			hpackDecoder = hpack.NewDecoder(4096, nil)
			h2framer = http2.NewFramer(nil, headerStream)
		})

		It("handles a sample GET request", func() {
			var handlerCalled bool
			s.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.Host).To(Equal("www.example.com"))
				handlerCalled = true
			})
			headerStream.Write([]byte{
				0x0, 0x0, 0x11, 0x1, 0x5, 0x0, 0x0, 0x0, 0x5,
				// Taken from https://http2.github.io/http2-spec/compression.html#request.examples.with.huffman.coding
				0x82, 0x86, 0x84, 0x41, 0x8c, 0xf1, 0xe3, 0xc2, 0xe5, 0xf2, 0x3a, 0x6b, 0xa0, 0xab, 0x90, 0xf4, 0xff,
			})
			err := s.handleRequest(session, headerStream, &sync.Mutex{}, hpackDecoder, h2framer)
			Expect(err).NotTo(HaveOccurred())
			Eventually(func() bool { return handlerCalled }).Should(BeTrue())
			Expect(dataStream.remoteClosed).To(BeTrue())
		})

		It("does not close the dataStream when end of stream is not set", func() {
			var handlerCalled bool
			s.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.Host).To(Equal("www.example.com"))
				handlerCalled = true
			})
			headerStream.Write([]byte{
				0x0, 0x0, 0x11, 0x1, 0x4, 0x0, 0x0, 0x0, 0x5,
				// Taken from https://http2.github.io/http2-spec/compression.html#request.examples.with.huffman.coding
				0x82, 0x86, 0x84, 0x41, 0x8c, 0xf1, 0xe3, 0xc2, 0xe5, 0xf2, 0x3a, 0x6b, 0xa0, 0xab, 0x90, 0xf4, 0xff,
			})
			err := s.handleRequest(session, headerStream, &sync.Mutex{}, hpackDecoder, h2framer)
			Expect(err).NotTo(HaveOccurred())
			Eventually(func() bool { return handlerCalled }).Should(BeTrue())
			Expect(dataStream.remoteClosed).To(BeFalse())
		})
	})

	It("handles the header stream", func() {
		var handlerCalled bool
		s.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Expect(r.Host).To(Equal("www.example.com"))
			handlerCalled = true
		})
		headerStream := &mockStream{id: 3}
		headerStream.Write([]byte{
			0x0, 0x0, 0x11, 0x1, 0x4, 0x0, 0x0, 0x0, 0x5,
			// Taken from https://http2.github.io/http2-spec/compression.html#request.examples.with.huffman.coding
			0x82, 0x86, 0x84, 0x41, 0x8c, 0xf1, 0xe3, 0xc2, 0xe5, 0xf2, 0x3a, 0x6b, 0xa0, 0xab, 0x90, 0xf4, 0xff,
		})
		s.handleStream(session, headerStream)
		Eventually(func() bool { return handlerCalled }).Should(BeTrue())
	})

	It("ignores other streams", func() {
		var handlerCalled bool
		s.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Expect(r.Host).To(Equal("www.example.com"))
			handlerCalled = true
		})
		headerStream := &mockStream{id: 5}
		headerStream.Write([]byte{
			0x0, 0x0, 0x11, 0x1, 0x4, 0x0, 0x0, 0x0, 0x5,
			// Taken from https://http2.github.io/http2-spec/compression.html#request.examples.with.huffman.coding
			0x82, 0x86, 0x84, 0x41, 0x8c, 0xf1, 0xe3, 0xc2, 0xe5, 0xf2, 0x3a, 0x6b, 0xa0, 0xab, 0x90, 0xf4, 0xff,
		})
		s.handleStream(session, headerStream)
		Consistently(func() bool { return handlerCalled }).Should(BeFalse())
	})

	It("supports closing after first request", func() {
		s.CloseAfterFirstRequest = true
		s.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
		headerStream := &mockStream{id: 3}
		headerStream.Write([]byte{
			0x0, 0x0, 0x11, 0x1, 0x4, 0x0, 0x0, 0x0, 0x5,
			// Taken from https://http2.github.io/http2-spec/compression.html#request.examples.with.huffman.coding
			0x82, 0x86, 0x84, 0x41, 0x8c, 0xf1, 0xe3, 0xc2, 0xe5, 0xf2, 0x3a, 0x6b, 0xa0, 0xab, 0x90, 0xf4, 0xff,
		})
		Expect(session.closed).To(BeFalse())
		s.handleStream(session, headerStream)
		Eventually(func() bool { return session.closed }).Should(BeTrue())
	})

	It("uses the default handler as fallback", func() {
		var handlerCalled bool
		http.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Expect(r.Host).To(Equal("www.example.com"))
			handlerCalled = true
		}))
		headerStream := &mockStream{id: 3}
		headerStream.Write([]byte{
			0x0, 0x0, 0x11, 0x1, 0x4, 0x0, 0x0, 0x0, 0x5,
			// Taken from https://http2.github.io/http2-spec/compression.html#request.examples.with.huffman.coding
			0x82, 0x86, 0x84, 0x41, 0x8c, 0xf1, 0xe3, 0xc2, 0xe5, 0xf2, 0x3a, 0x6b, 0xa0, 0xab, 0x90, 0xf4, 0xff,
		})
		s.handleStream(session, headerStream)
		Eventually(func() bool { return handlerCalled }).Should(BeTrue())
	})

	It("should panic when Serve() is called", func() {
		Expect(func() {
			Server{}.Serve(nil)
		}).To(Panic())
	})

	Context("setting http headers", func() {
		expected := http.Header{
			"Alt-Svc":            {`quic=":443"; ma=2592000; v="33,32,31,30"`},
			"Alternate-Protocol": {`443:quic`},
		}

		It("sets proper headers with numeric port", func() {
			s.Server.Addr = ":443"
			hdr := http.Header{}
			err := s.SetQuicHeaders(hdr)
			Expect(err).NotTo(HaveOccurred())
			Expect(hdr).To(Equal(expected))
		})

		It("sets proper headers with full addr", func() {
			s.Server.Addr = "127.0.0.1:443"
			hdr := http.Header{}
			err := s.SetQuicHeaders(hdr)
			Expect(err).NotTo(HaveOccurred())
			Expect(hdr).To(Equal(expected))
		})

		It("sets proper headers with string port", func() {
			s.Server.Addr = ":https"
			hdr := http.Header{}
			err := s.SetQuicHeaders(hdr)
			Expect(err).NotTo(HaveOccurred())
			Expect(hdr).To(Equal(expected))
		})

		It("works multiple times", func() {
			s.Server.Addr = ":https"
			hdr := http.Header{}
			err := s.SetQuicHeaders(hdr)
			Expect(err).NotTo(HaveOccurred())
			Expect(hdr).To(Equal(expected))
			hdr = http.Header{}
			err = s.SetQuicHeaders(hdr)
			Expect(err).NotTo(HaveOccurred())
			Expect(hdr).To(Equal(expected))
		})
	})

	It("should error when ListenAndServe is called with s.Server nil", func() {
		err := (&Server{}).ListenAndServe()
		Expect(err).To(MatchError("use of h2quic.Server without http.Server"))
	})

	It("should error when ListenAndServeTLS is called with s.Server nil", func() {
		err := (&Server{}).ListenAndServeTLS("", "")
		Expect(err).To(MatchError("use of h2quic.Server without http.Server"))
	})

	It("should nop-Close() when s.server is nil", func() {
		err := (&Server{}).Close()
		Expect(err).NotTo(HaveOccurred())
	})

	Context("ListenAndServe", func() {
		BeforeEach(func() {
			s.Server.Addr = addr
		})

		AfterEach(func() {
			err := s.Close()
			Expect(err).NotTo(HaveOccurred())
		})

		It("works", func(done Done) {
			go func() {
				defer GinkgoRecover()
				err := s.ListenAndServe()
				Expect(err).NotTo(HaveOccurred())
				close(done)
			}()
			time.Sleep(10 * time.Millisecond)
			err := s.Close()
			Expect(err).NotTo(HaveOccurred())
		}, 0.5)

		It("may only be called once", func(done Done) {
			go func() {
				defer GinkgoRecover()
				err := s.ListenAndServe()
				Expect(err).NotTo(HaveOccurred())
				close(done)
			}()
			time.Sleep(10 * time.Millisecond)
			err := s.ListenAndServe()
			Expect(err).To(MatchError("ListenAndServe may only be called once"))
			err = s.Close()
			Expect(err).NotTo(HaveOccurred())
		}, 0.5)
	})

	Context("ListenAndServeTLS", func() {
		path := os.Getenv("GOPATH")
		path += "/src/github.com/lucas-clemente/quic-go/example/"

		BeforeEach(func() {
			s.Server.Addr = addr
		})

		AfterEach(func() {
			err := s.Close()
			Expect(err).NotTo(HaveOccurred())
		})

		It("works", func(done Done) {
			go func() {
				defer GinkgoRecover()
				err := s.ListenAndServeTLS(path+"fullchain.pem", path+"privkey.pem")
				Expect(err).NotTo(HaveOccurred())
				close(done)
			}()
			time.Sleep(10 * time.Millisecond)
			err := s.Close()
			Expect(err).NotTo(HaveOccurred())
		}, 0.5)

		It("may only be called once", func(done Done) {
			go func() {
				defer GinkgoRecover()
				err := s.ListenAndServeTLS(path+"fullchain.pem", path+"privkey.pem")
				Expect(err).NotTo(HaveOccurred())
				close(done)
			}()
			time.Sleep(10 * time.Millisecond)
			err := s.ListenAndServeTLS(path+"fullchain.pem", path+"privkey.pem")
			Expect(err).To(MatchError("ListenAndServe may only be called once"))
			err = s.Close()
			Expect(err).NotTo(HaveOccurred())
		}, 0.5)
	})

	It("closes gracefully", func() {
		err := s.CloseGracefully(0)
		Expect(err).NotTo(HaveOccurred())
	})
})
