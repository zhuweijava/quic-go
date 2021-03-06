package handshake

import (
	"bytes"
	"errors"
	"net"

	"github.com/lucas-clemente/quic-go/crypto"
	"github.com/lucas-clemente/quic-go/protocol"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

type mockKEX struct {
	ephermal bool
}

func (m *mockKEX) PublicKey() []byte {
	if m.ephermal {
		return []byte("ephermal pub")
	}
	return []byte("initial public")
}

func (m *mockKEX) CalculateSharedKey(otherPublic []byte) ([]byte, error) {
	if m.ephermal {
		return []byte("shared ephermal"), nil
	}
	return []byte("shared key"), nil
}

type mockSigner struct {
	gotCHLO bool
}

func (s *mockSigner) SignServerProof(sni string, chlo []byte, serverConfigData []byte) ([]byte, error) {
	if len(chlo) > 0 {
		s.gotCHLO = true
	}
	return []byte("proof"), nil
}
func (*mockSigner) GetCertsCompressed(sni string, common, cached []byte) ([]byte, error) {
	return []byte("certcompressed"), nil
}
func (*mockSigner) GetLeafCert(sni string) ([]byte, error) {
	return []byte("certuncompressed"), nil
}

type mockAEAD struct {
	forwardSecure bool
	sharedSecret  []byte
}

func (m *mockAEAD) Seal(packetNumber protocol.PacketNumber, associatedData []byte, plaintext []byte) []byte {
	if m.forwardSecure {
		return []byte("forward secure encrypted")
	}
	return []byte("encrypted")
}

func (m *mockAEAD) Open(packetNumber protocol.PacketNumber, associatedData []byte, ciphertext []byte) ([]byte, error) {
	if m.forwardSecure && string(ciphertext) == "forward secure encrypted" {
		return []byte("decrypted"), nil
	} else if !m.forwardSecure && string(ciphertext) == "encrypted" {
		return []byte("decrypted"), nil
	}
	return nil, errors.New("authentication failed")
}

func (mockAEAD) DiversificationNonce() []byte { return nil }

var expectedInitialNonceLen int
var expectedFSNonceLen int

func mockKeyDerivation(v protocol.VersionNumber, forwardSecure bool, sharedSecret, nonces []byte, connID protocol.ConnectionID, chlo []byte, scfg []byte, cert []byte, divNonce []byte) (crypto.AEAD, error) {
	if forwardSecure {
		Expect(nonces).To(HaveLen(expectedFSNonceLen))
	} else {
		Expect(nonces).To(HaveLen(expectedInitialNonceLen))
	}
	return &mockAEAD{forwardSecure: forwardSecure, sharedSecret: sharedSecret}, nil
}

type mockStream struct {
	dataToRead  bytes.Buffer
	dataWritten bytes.Buffer
}

func (s *mockStream) Read(p []byte) (int, error) {
	return s.dataToRead.Read(p)
}

func (s *mockStream) ReadByte() (byte, error) {
	return s.dataToRead.ReadByte()
}

func (s *mockStream) Write(p []byte) (int, error) {
	return s.dataWritten.Write(p)
}

func (s *mockStream) Close() error                       { panic("not implemented") }
func (mockStream) CloseRemote(offset protocol.ByteCount) { panic("not implemented") }
func (s mockStream) StreamID() protocol.StreamID         { panic("not implemented") }

type mockStkSource struct{}

func (mockStkSource) NewToken(ip net.IP) ([]byte, error) {
	return append([]byte("token "), ip...), nil
}

func (mockStkSource) VerifyToken(ip net.IP, token []byte) error {
	split := bytes.Split(token, []byte(" "))
	if len(split) != 2 {
		return errors.New("stk required")
	}
	if !bytes.Equal(split[0], []byte("token")) {
		return errors.New("no prefix match")
	}
	if !bytes.Equal(split[1], ip) {
		return errors.New("ip wrong")
	}
	return nil
}

var _ = Describe("Crypto setup", func() {
	var (
		kex         *mockKEX
		signer      *mockSigner
		scfg        *ServerConfig
		cs          *CryptoSetup
		stream      *mockStream
		cpm         *ConnectionParametersManager
		aeadChanged chan struct{}
		nonce32     []byte
		ip          net.IP
		validSTK    []byte
	)

	BeforeEach(func() {
		var err error
		ip = net.ParseIP("1.2.3.4")
		validSTK, err = mockStkSource{}.NewToken(ip)
		Expect(err).NotTo(HaveOccurred())
		nonce32 = make([]byte, 32)
		expectedInitialNonceLen = 32
		expectedFSNonceLen = 64
		aeadChanged = make(chan struct{}, 1)
		stream = &mockStream{}
		kex = &mockKEX{}
		signer = &mockSigner{}
		scfg, err = NewServerConfig(kex, signer)
		Expect(err).NotTo(HaveOccurred())
		scfg.stkSource = &mockStkSource{}
		v := protocol.SupportedVersions[len(protocol.SupportedVersions)-1]
		cpm = NewConnectionParamatersManager()
		cs, err = NewCryptoSetup(protocol.ConnectionID(42), ip, v, scfg, stream, cpm, aeadChanged)
		Expect(err).NotTo(HaveOccurred())
		cs.keyDerivation = mockKeyDerivation
		cs.keyExchange = func() (crypto.KeyExchange, error) { return &mockKEX{ephermal: true}, nil }
	})

	It("has a nonce", func() {
		Expect(cs.nonce).To(HaveLen(32))
		s := 0
		for _, b := range cs.nonce {
			s += int(b)
		}
		Expect(s).ToNot(BeZero())
	})

	Context("diversification nonce", func() {
		BeforeEach(func() {
			cs.version = 33
			cs.secureAEAD = &mockAEAD{}
			cs.receivedForwardSecurePacket = false
		})

		It("returns diversification nonces", func() {
			Expect(cs.DiversificationNonce()).To(HaveLen(32))
		})

		It("does not return nonce for version < 33", func() {
			cs.version = 32
			Expect(cs.DiversificationNonce()).To(BeEmpty())
		})

		It("does not return nonce for FS packets", func() {
			cs.receivedForwardSecurePacket = true
			Expect(cs.DiversificationNonce()).To(BeEmpty())
		})

		It("does not return nonce for unencrypted packets", func() {
			cs.secureAEAD = nil
			Expect(cs.DiversificationNonce()).To(BeEmpty())
		})
	})

	Context("when responding to client messages", func() {
		It("generates REJ messages", func() {
			response, err := cs.handleInchoateCHLO("", bytes.Repeat([]byte{'a'}, protocol.ClientHelloMinimumSize), nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(response).To(HavePrefix("REJ"))
			Expect(response).To(ContainSubstring("certcompressed"))
			Expect(response).To(ContainSubstring("initial public"))
			Expect(signer.gotCHLO).To(BeTrue())
		})

		It("generates REJ messages for version 30", func() {
			cs.version = protocol.VersionNumber(30)
			_, err := cs.handleInchoateCHLO("", sampleCHLO, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(signer.gotCHLO).To(BeFalse())
		})

		It("generates SHLO messages", func() {
			response, err := cs.handleCHLO("", []byte("chlo-data"), map[Tag][]byte{
				TagPUBS: []byte("pubs-c"),
				TagNONC: nonce32,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(response).To(HavePrefix("SHLO"))
			Expect(response).To(ContainSubstring("ephermal pub"))
			Expect(response).To(ContainSubstring(string(cs.nonce)))
			Expect(response).To(ContainSubstring(string(protocol.SupportedVersionsAsTags)))
			Expect(cs.secureAEAD).ToNot(BeNil())
			Expect(cs.secureAEAD.(*mockAEAD).forwardSecure).To(BeFalse())
			Expect(cs.secureAEAD.(*mockAEAD).sharedSecret).To(Equal([]byte("shared key")))
			Expect(cs.forwardSecureAEAD).ToNot(BeNil())
			Expect(cs.forwardSecureAEAD.(*mockAEAD).sharedSecret).To(Equal([]byte("shared ephermal")))
			Expect(cs.forwardSecureAEAD.(*mockAEAD).forwardSecure).To(BeTrue())
		})

		It("handles long handshake", func() {
			WriteHandshakeMessage(&stream.dataToRead, TagCHLO, map[Tag][]byte{
				TagSNI: []byte("quic.clemente.io"),
				TagSTK: validSTK,
				TagPAD: bytes.Repeat([]byte{'a'}, protocol.ClientHelloMinimumSize),
			})
			WriteHandshakeMessage(&stream.dataToRead, TagCHLO, map[Tag][]byte{
				TagSCID: scfg.ID,
				TagSNI:  []byte("quic.clemente.io"),
				TagNONC: nonce32,
				TagSTK:  validSTK,
			})
			err := cs.HandleCryptoStream()
			Expect(err).NotTo(HaveOccurred())
			Expect(stream.dataWritten.Bytes()).To(HavePrefix("REJ"))
			Expect(stream.dataWritten.Bytes()).To(ContainSubstring("SHLO"))
			Expect(aeadChanged).To(Receive())
		})

		It("handles 0-RTT handshake", func() {
			WriteHandshakeMessage(&stream.dataToRead, TagCHLO, map[Tag][]byte{
				TagSCID: scfg.ID,
				TagSNI:  []byte("quic.clemente.io"),
				TagNONC: nonce32,
				TagSTK:  validSTK,
			})
			err := cs.HandleCryptoStream()
			Expect(err).NotTo(HaveOccurred())
			Expect(stream.dataWritten.Bytes()).To(HavePrefix("SHLO"))
			Expect(stream.dataWritten.Bytes()).ToNot(ContainSubstring("REJ"))
			Expect(aeadChanged).To(Receive())
		})

		It("recognizes inchoate CHLOs missing SCID", func() {
			Expect(cs.isInchoateCHLO(map[Tag][]byte{})).To(BeTrue())
		})

		It("recognizes proper CHLOs", func() {
			Expect(cs.isInchoateCHLO(map[Tag][]byte{TagSCID: scfg.ID})).To(BeFalse())
		})

		It("errors on too short inchoate CHLOs", func() {
			_, err := cs.handleInchoateCHLO("", bytes.Repeat([]byte{'a'}, protocol.ClientHelloMinimumSize-1), nil)
			Expect(err).To(MatchError("CryptoInvalidValueLength: CHLO too small"))
		})
	})

	It("errors without SNI", func() {
		WriteHandshakeMessage(&stream.dataToRead, TagCHLO, map[Tag][]byte{
			TagSTK: validSTK,
		})
		err := cs.HandleCryptoStream()
		Expect(err).To(MatchError("CryptoMessageParameterNotFound: SNI required"))
	})

	Context("escalating crypto", func() {
		foobarFNVSigned := []byte{0x18, 0x6f, 0x44, 0xba, 0x97, 0x35, 0xd, 0x6f, 0xbf, 0x64, 0x3c, 0x79, 0x66, 0x6f, 0x6f, 0x62, 0x61, 0x72}

		doCHLO := func() {
			_, err := cs.handleCHLO("", []byte("chlo-data"), map[Tag][]byte{TagPUBS: []byte("pubs-c"), TagNONC: nonce32})
			Expect(err).ToNot(HaveOccurred())
		}

		Context("null encryption", func() {
			It("is used initially", func() {
				Expect(cs.Seal(0, []byte{}, []byte("foobar"))).To(Equal(foobarFNVSigned))
			})

			It("is accepted initially", func() {
				d, err := cs.Open(0, []byte{}, foobarFNVSigned)
				Expect(err).ToNot(HaveOccurred())
				Expect(d).To(Equal([]byte("foobar")))
			})

			It("is still accepted after CHLO", func() {
				doCHLO()
				Expect(cs.secureAEAD).ToNot(BeNil())
				_, err := cs.Open(0, []byte{}, foobarFNVSigned)
				Expect(err).ToNot(HaveOccurred())
			})

			It("is not accepted after receiving secure packet", func() {
				doCHLO()
				Expect(cs.secureAEAD).ToNot(BeNil())
				d, err := cs.Open(0, []byte{}, []byte("encrypted"))
				Expect(err).ToNot(HaveOccurred())
				Expect(d).To(Equal([]byte("decrypted")))
				_, err = cs.Open(0, []byte{}, foobarFNVSigned)
				Expect(err).To(MatchError("authentication failed"))
			})

			It("is not used after CHLO", func() {
				doCHLO()
				d := cs.Seal(0, []byte{}, []byte("foobar"))
				Expect(d).ToNot(Equal(foobarFNVSigned))
			})
		})

		Context("initial encryption", func() {
			It("is used after CHLO", func() {
				doCHLO()
				d := cs.Seal(0, []byte{}, []byte("foobar"))
				Expect(d).To(Equal([]byte("encrypted")))
			})

			It("is accepted after CHLO", func() {
				doCHLO()
				d, err := cs.Open(0, []byte{}, []byte("encrypted"))
				Expect(err).ToNot(HaveOccurred())
				Expect(d).To(Equal([]byte("decrypted")))
			})

			It("is not used after receiving forward secure packet", func() {
				doCHLO()
				_, err := cs.Open(0, []byte{}, []byte("forward secure encrypted"))
				Expect(err).ToNot(HaveOccurred())
				d := cs.Seal(0, []byte{}, []byte("foobar"))
				Expect(d).To(Equal([]byte("forward secure encrypted")))
			})

			It("is not accepted after receiving forward secure packet", func() {
				doCHLO()
				_, err := cs.Open(0, []byte{}, []byte("forward secure encrypted"))
				Expect(err).ToNot(HaveOccurred())
				_, err = cs.Open(0, []byte{}, []byte("encrypted"))
				Expect(err).To(MatchError("authentication failed"))
			})
		})

		Context("forward secure encryption", func() {
			It("is used after receiving forward secure packet", func() {
				doCHLO()
				_, err := cs.Open(0, []byte{}, []byte("forward secure encrypted"))
				Expect(err).ToNot(HaveOccurred())
				d := cs.Seal(0, []byte{}, []byte("foobar"))
				Expect(d).To(Equal([]byte("forward secure encrypted")))
			})
		})
	})

	Context("STK verification and creation", func() {
		It("requires STK", func() {
			done, err := cs.handleMessage(bytes.Repeat([]byte{'a'}, protocol.ClientHelloMinimumSize), map[Tag][]byte{
				TagSNI: []byte("foo"),
			})
			Expect(done).To(BeFalse())
			Expect(err).To(BeNil())
			Expect(stream.dataWritten.Bytes()).To(ContainSubstring(string(validSTK)))
		})

		It("works with proper STK", func() {
			done, err := cs.handleMessage(bytes.Repeat([]byte{'a'}, protocol.ClientHelloMinimumSize), map[Tag][]byte{
				TagSTK: validSTK,
				TagSNI: []byte("foo"),
			})
			Expect(done).To(BeFalse())
			Expect(err).To(BeNil())
		})

		It("errors if IP does not match", func() {
			done, err := cs.handleMessage(bytes.Repeat([]byte{'a'}, protocol.ClientHelloMinimumSize), map[Tag][]byte{
				TagSNI: []byte("foo"),
				TagSTK: []byte("token \x04\x03\x03\x01"),
			})
			Expect(done).To(BeFalse())
			Expect(err).To(BeNil())
			Expect(stream.dataWritten.Bytes()).To(ContainSubstring(string(validSTK)))
		})
	})
})
