package quic

import (
	"github.com/lucas-clemente/quic-go/frames"
	"github.com/lucas-clemente/quic-go/protocol"
	"github.com/lucas-clemente/quic-go/qerr"
	"github.com/lucas-clemente/quic-go/utils"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

func compareGapValues(gapList *utils.ByteIntervalList, expectedGaps []utils.ByteInterval) {
	Expect(gapList.Len()).To(Equal(len(expectedGaps)))
	var i int
	for gap := gapList.Front(); gap != nil; gap = gap.Next() {
		Expect(gap.Value).To(Equal(expectedGaps[i]))
		i++
	}
}

var _ = Describe("StreamFrame sorter", func() {
	var s *streamFrameSorter

	BeforeEach(func() {
		s = newStreamFrameSorter()
	})

	It("head returns nil when empty", func() {
		Expect(s.Head()).To(BeNil())
	})

	Context("Push", func() {
		It("inserts and pops a single frame", func() {
			f := &frames.StreamFrame{
				Offset: 0,
				Data:   []byte("foobar"),
			}
			err := s.Push(f)
			Expect(err).ToNot(HaveOccurred())
			Expect(s.Head()).To(Equal(f))
			Expect(s.Pop()).To(Equal(f))
			Expect(s.Head()).To(BeNil())
		})

		It("inserts and pops two consecutive frame", func() {
			f1 := &frames.StreamFrame{
				Offset: 0,
				Data:   []byte("foobar"),
			}
			f2 := &frames.StreamFrame{
				Offset: 6,
				Data:   []byte("foobar2"),
			}
			err := s.Push(f1)
			Expect(err).ToNot(HaveOccurred())
			err = s.Push(f2)
			Expect(err).ToNot(HaveOccurred())
			Expect(s.Pop()).To(Equal(f1))
			Expect(s.Pop()).To(Equal(f2))
			Expect(s.Head()).To(BeNil())
		})

		It("rejects empty frames", func() {
			f := &frames.StreamFrame{}
			err := s.Push(f)
			Expect(err).To(MatchError(errEmptyStreamData))
		})

		Context("FinBit handling", func() {
			It("saves a FinBit frame at offset 0", func() {
				f := &frames.StreamFrame{
					Offset: 0,
					FinBit: true,
				}
				err := s.Push(f)
				Expect(err).ToNot(HaveOccurred())
				Expect(s.Head()).To(Equal(f))
			})

			It("sets the FinBit if a stream is closed after receiving some data", func() {
				f1 := &frames.StreamFrame{
					Offset: 0,
					Data:   []byte("foobar"),
				}
				err := s.Push(f1)
				Expect(err).ToNot(HaveOccurred())
				f2 := &frames.StreamFrame{
					Offset: 6,
					FinBit: true,
				}
				err = s.Push(f2)
				Expect(err).ToNot(HaveOccurred())
				Expect(s.Pop()).To(Equal(f1))
				Expect(s.Pop()).To(Equal(f2))
			})
		})

		Context("Gap handling", func() {
			It("finds the first gap", func() {
				f := &frames.StreamFrame{
					Offset: 10,
					Data:   []byte("foobar"),
				}
				err := s.Push(f)
				Expect(err).ToNot(HaveOccurred())
				Expect(s.gaps.Len()).To(Equal(2))
				Expect(s.gaps.Front().Value).To(Equal(utils.ByteInterval{Start: 0, End: 10}))
			})

			It("correctly sets the first gap for a frame with offset 0", func() {
				f := &frames.StreamFrame{
					Offset: 0,
					Data:   []byte("foobar"),
				}
				err := s.Push(f)
				Expect(err).ToNot(HaveOccurred())
				Expect(s.gaps.Len()).To(Equal(1))
				Expect(s.gaps.Front().Value).To(Equal(utils.ByteInterval{Start: 6, End: protocol.MaxByteCount}))
			})

			It("finds the two gaps", func() {
				f1 := &frames.StreamFrame{
					Offset: 10,
					Data:   []byte("foobar"),
				}
				err := s.Push(f1)
				Expect(err).ToNot(HaveOccurred())
				f2 := &frames.StreamFrame{
					Offset: 20,
					Data:   []byte("foobar"),
				}
				err = s.Push(f2)
				Expect(err).ToNot(HaveOccurred())
				Expect(s.gaps.Len()).To(Equal(3))
				el := s.gaps.Front() // first gap
				Expect(el.Value).To(Equal(utils.ByteInterval{Start: 0, End: 10}))
				el = el.Next() // second gap
				Expect(el.Value).To(Equal(utils.ByteInterval{Start: 16, End: 20}))
				Expect(s.gaps.Back().Value.Start).To(Equal(protocol.ByteCount(26)))
			})

			It("finds the two gaps in reverse order", func() {
				f1 := &frames.StreamFrame{
					Offset: 20,
					Data:   []byte("foobar"),
				}
				err := s.Push(f1)
				Expect(err).ToNot(HaveOccurred())
				f2 := &frames.StreamFrame{
					Offset: 10,
					Data:   []byte("foobar"),
				}
				err = s.Push(f2)
				Expect(err).ToNot(HaveOccurred())
				Expect(s.gaps.Len()).To(Equal(3))
				el := s.gaps.Front() // first gap
				Expect(el.Value).To(Equal(utils.ByteInterval{Start: 0, End: 10}))
				el = el.Next() // second gap
				Expect(el.Value).To(Equal(utils.ByteInterval{Start: 16, End: 20}))
				Expect(s.gaps.Back().Value).To(Equal(utils.ByteInterval{Start: 26, End: protocol.MaxByteCount}))
			})

			It("shrinks a gap when it is partially filled", func() {
				f1 := &frames.StreamFrame{
					Offset: 10,
					Data:   []byte("test"),
				}
				err := s.Push(f1)
				Expect(err).ToNot(HaveOccurred())
				f2 := &frames.StreamFrame{
					Offset: 4,
					Data:   []byte("foobar"),
				}
				err = s.Push(f2)
				Expect(err).ToNot(HaveOccurred())
				Expect(s.gaps.Len()).To(Equal(2))
				Expect(s.gaps.Front().Value).To(Equal(utils.ByteInterval{Start: 0, End: 4}))
				Expect(s.gaps.Back().Value).To(Equal(utils.ByteInterval{Start: 14, End: protocol.MaxByteCount}))
			})

			It("deletes a gap at the beginning, when it is filled", func() {
				f1 := &frames.StreamFrame{
					Offset: 6,
					Data:   []byte("test"),
				}
				err := s.Push(f1)
				Expect(err).ToNot(HaveOccurred())
				f2 := &frames.StreamFrame{
					Offset: 0,
					Data:   []byte("foobar"),
				}
				err = s.Push(f2)
				Expect(err).ToNot(HaveOccurred())
				Expect(s.gaps.Len()).To(Equal(1))
				Expect(s.gaps.Front().Value).To(Equal(utils.ByteInterval{Start: 10, End: protocol.MaxByteCount}))
			})

			It("deletes a gap in the middle, when it is filled", func() {
				f1 := &frames.StreamFrame{
					Offset: 0,
					Data:   []byte("test"),
				}
				err := s.Push(f1)
				Expect(err).ToNot(HaveOccurred())
				f2 := &frames.StreamFrame{
					Offset: 10,
					Data:   []byte("test2"),
				}
				err = s.Push(f2)
				Expect(err).ToNot(HaveOccurred())
				f3 := &frames.StreamFrame{
					Offset: 4,
					Data:   []byte("foobar"),
				}
				err = s.Push(f3)
				Expect(err).ToNot(HaveOccurred())
				Expect(s.gaps.Len()).To(Equal(1))
				Expect(s.gaps.Front().Value).To(Equal(utils.ByteInterval{Start: 15, End: protocol.MaxByteCount}))
				Expect(s.queuedFrames).To(HaveLen(3))
			})

			It("splits a gap into two", func() {
				f1 := &frames.StreamFrame{
					Offset: 100,
					Data:   []byte("test"),
				}
				err := s.Push(f1)
				Expect(err).ToNot(HaveOccurred())
				f2 := &frames.StreamFrame{
					Offset: 50,
					Data:   []byte("foobar"),
				}
				err = s.Push(f2)
				Expect(err).ToNot(HaveOccurred())
				Expect(s.gaps.Len()).To(Equal(3))
				el := s.gaps.Front() // first gap
				Expect(el.Value).To(Equal(utils.ByteInterval{Start: 0, End: 50}))
				el = el.Next() // second gap
				Expect(el.Value).To(Equal(utils.ByteInterval{Start: 56, End: 100}))
				Expect(s.gaps.Back().Value).To(Equal(utils.ByteInterval{Start: 104, End: protocol.MaxByteCount}))
				Expect(s.queuedFrames).To(HaveLen(2))
			})

			Context("Overlapping Stream Data detection", func() {
				var expectedGaps []utils.ByteInterval
				BeforeEach(func() {
					// create gaps: 0-5, 10-15, 15-20, 30-inf
					expectedGaps = expectedGaps[:0]
					expectedGaps = append(expectedGaps, utils.ByteInterval{Start: 0, End: 5})
					err := s.Push(&frames.StreamFrame{Offset: 5, Data: []byte("12345")})
					Expect(err).ToNot(HaveOccurred())
					expectedGaps = append(expectedGaps, utils.ByteInterval{Start: 10, End: 15})
					err = s.Push(&frames.StreamFrame{Offset: 15, Data: []byte("12345")})
					Expect(err).ToNot(HaveOccurred())
					expectedGaps = append(expectedGaps, utils.ByteInterval{Start: 20, End: 25})
					err = s.Push(&frames.StreamFrame{Offset: 25, Data: []byte("12345")})
					Expect(err).ToNot(HaveOccurred())
					expectedGaps = append(expectedGaps, utils.ByteInterval{Start: 30, End: protocol.MaxByteCount})
				})

				It("rejects a frame with offset 0 that overlaps at the end", func() {
					f := &frames.StreamFrame{
						Offset: 0,
						Data:   []byte("foobar"),
					}
					err := s.Push(f)
					Expect(err).To(MatchError(qerr.Error(qerr.OverlappingStreamData, "")))
					Expect(s.queuedFrames).ToNot(HaveKey(protocol.ByteCount(0)))
					compareGapValues(s.gaps, expectedGaps)
				})

				It("rejects a frame that overlaps at the end", func() {
					// 4 to 6
					f := &frames.StreamFrame{
						Offset: 4,
						Data:   []byte("12"),
					}
					err := s.Push(f)
					Expect(err).To(MatchError(errOverlappingStreamData))
					Expect(s.queuedFrames).ToNot(HaveKey(protocol.ByteCount(4)))
					compareGapValues(s.gaps, expectedGaps)
				})

				It("rejects a frame that completely fills a gap, but overlaps at the end", func() {
					// 10 to 16
					f := &frames.StreamFrame{
						Offset: 10,
						Data:   []byte("foobar"),
					}
					err := s.Push(f)
					Expect(err).To(MatchError(errOverlappingStreamData))
					Expect(s.queuedFrames).ToNot(HaveKey(protocol.ByteCount(10)))
					compareGapValues(s.gaps, expectedGaps)
				})

				It("rejects a frame that overlaps at the beginning", func() {
					// 8 to 14
					f := &frames.StreamFrame{
						Offset: 8,
						Data:   []byte("foobar"),
					}
					err := s.Push(f)
					Expect(err).To(MatchError(errOverlappingStreamData))
					Expect(s.queuedFrames).ToNot(HaveKey(protocol.ByteCount(8)))
					compareGapValues(s.gaps, expectedGaps)
				})

				It("rejects a frame that overlaps at the beginning and at the end, starting in a gap", func() {
					// 2 to 11
					f := &frames.StreamFrame{
						Offset: 2,
						Data:   []byte("123456789"),
					}
					err := s.Push(f)
					Expect(err).To(MatchError(errOverlappingStreamData))
					Expect(s.queuedFrames).ToNot(HaveKey(protocol.ByteCount(2)))
					compareGapValues(s.gaps, expectedGaps)
				})

				It("rejects a frame that overlaps at the beginning and at the end, starting in data already received", func() {
					// 8 to 17
					f := &frames.StreamFrame{
						Offset: 8,
						Data:   []byte("123456789"),
					}
					err := s.Push(f)
					Expect(err).To(MatchError(errOverlappingStreamData))
					Expect(s.queuedFrames).ToNot(HaveKey(protocol.ByteCount(8)))
					compareGapValues(s.gaps, expectedGaps)
				})

				It("rejects a frame that completely covers two gaps", func() {
					// 10 to 20
					f := &frames.StreamFrame{
						Offset: 10,
						Data:   []byte("1234567890"),
					}
					err := s.Push(f)
					Expect(err).To(MatchError(errOverlappingStreamData))
					Expect(s.queuedFrames).ToNot(HaveKey(protocol.ByteCount(10)))
					compareGapValues(s.gaps, expectedGaps)
				})
			})

			Context("Duplicate data detection", func() {
				var expectedGaps []utils.ByteInterval

				BeforeEach(func() {
					// create gaps: 5-10, 15-20, 25-inf
					expectedGaps = expectedGaps[:0]
					expectedGaps = append(expectedGaps, utils.ByteInterval{Start: 5, End: 10})
					err := s.Push(&frames.StreamFrame{Offset: 0, Data: []byte("12345")})
					Expect(err).ToNot(HaveOccurred())
					expectedGaps = append(expectedGaps, utils.ByteInterval{Start: 15, End: protocol.MaxByteCount})
					err = s.Push(&frames.StreamFrame{Offset: 10, Data: []byte("12345")})
					Expect(err).ToNot(HaveOccurred())
				})

				It("detects a complete duplicate frame", func() {
					err := s.Push(&frames.StreamFrame{Offset: 0, Data: []byte("12345")})
					Expect(err).To(MatchError(errDuplicateStreamData))
					compareGapValues(s.gaps, expectedGaps)
				})

				It("does not modify data when receiving a duplicate", func() {
					err := s.Push(&frames.StreamFrame{Offset: 0, Data: []byte("67890")})
					Expect(err).To(MatchError(errDuplicateStreamData))
					Expect(s.queuedFrames[0].Data).To(Equal([]byte("12345")))
					compareGapValues(s.gaps, expectedGaps)
				})

				It("detects a duplicate frame that is smaller than the original, starting at the beginning", func() {
					// 10 to 12
					err := s.Push(&frames.StreamFrame{Offset: 10, Data: []byte("12")})
					Expect(err).To(MatchError(errDuplicateStreamData))
					Expect(s.queuedFrames[10].DataLen()).To(Equal(protocol.ByteCount(5)))
					compareGapValues(s.gaps, expectedGaps)
				})

				It("detects a duplicate frame that is smaller than the original, somewhere in the middle", func() {
					// 1 to 4
					err := s.Push(&frames.StreamFrame{Offset: 1, Data: []byte("123")})
					Expect(err).To(MatchError(errDuplicateStreamData))
					Expect(s.queuedFrames[0].DataLen()).To(Equal(protocol.ByteCount(5)))
					Expect(s.queuedFrames).ToNot(HaveKey(protocol.ByteCount(1)))
					compareGapValues(s.gaps, expectedGaps)
				})

				It("detects a duplicate frame that is smaller than the original, with aligned end", func() {
					// 3 to 5
					err := s.Push(&frames.StreamFrame{Offset: 3, Data: []byte("12")})
					Expect(err).To(MatchError(errDuplicateStreamData))
					Expect(s.queuedFrames[0].DataLen()).To(Equal(protocol.ByteCount(5)))
					Expect(s.queuedFrames).ToNot(HaveKey(protocol.ByteCount(8)))
					compareGapValues(s.gaps, expectedGaps)
				})
			})

			Context("DOS protection", func() {
				It("errors when too many gaps are created", func() {
					for i := 0; i < protocol.MaxStreamFrameSorterGaps; i++ {
						f := &frames.StreamFrame{
							Data:   []byte("foobar"),
							Offset: protocol.ByteCount(i * 7),
						}
						err := s.Push(f)
						Expect(err).ToNot(HaveOccurred())
					}
					Expect(s.gaps.Len()).To(Equal(protocol.MaxStreamFrameSorterGaps))
					f := &frames.StreamFrame{
						Data:   []byte("foobar"),
						Offset: protocol.ByteCount(protocol.MaxStreamFrameSorterGaps*7) + 100,
					}
					err := s.Push(f)
					Expect(err).To(MatchError(errTooManyGapsInReceivedStreamData))
				})
			})
		})
	})
})
