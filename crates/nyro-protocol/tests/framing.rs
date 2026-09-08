use nyro_protocol::framing::Decoder;
#[test]
fn fragmented_unicode_and_line_endings() {
    for ending in ["\n", "\r\n", "\r"] {
        let bytes = format!(
            ": comment{ending}event: chunk{ending}data: 你好{ending}data: next{ending}{ending}"
        );
        let mut decoder = Decoder::new(1024);
        let mut events = Vec::new();
        for byte in bytes.as_bytes() {
            events.extend(decoder.push(&[*byte]).unwrap());
        }
        events.extend(decoder.finish().unwrap());
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].data, "你好\nnext");
        assert_eq!(events[0].event.as_deref(), Some("chunk"));
    }
}
#[test]
fn rejects_oversized_invalid_and_truncated_frames() {
    assert!(Decoder::new(8).push(b"data: 123456").is_err());
    assert!(Decoder::new(64).push(b"data: \xff\n\n").is_err());
    let mut decoder = Decoder::new(64);
    decoder.push(b"data: unfinished\n").unwrap();
    assert!(decoder.finish().is_err());
    let mut decoder = Decoder::new(16);
    assert_eq!(decoder.push(b"data: a\n\ndata: b\n\n").unwrap().len(), 2);
}
#[test]
fn crlf_bytes_count_towards_the_frame_limit() {
    let mut decoder = Decoder::new(10);
    assert!(decoder.push(b"data: a\r\n\r\n").is_err());
}
