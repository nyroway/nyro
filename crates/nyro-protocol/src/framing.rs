//! Incremental SSE framing. A frame must end with a blank line, including at EOF.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Event {
    pub data: String,
    pub event: Option<String>,
}
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FramingError(pub &'static str);
impl std::fmt::Display for FramingError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.0)
    }
}
impl std::error::Error for FramingError {}
pub struct Decoder {
    max_bytes: usize,
    frame_bytes: usize,
    line: Vec<u8>,
    data: Vec<String>,
    event: Option<String>,
    after_cr: bool,
    failed: bool,
}
impl Decoder {
    pub fn new(max_bytes: usize) -> Self {
        Self {
            max_bytes,
            frame_bytes: 0,
            line: Vec::new(),
            data: Vec::new(),
            event: None,
            after_cr: false,
            failed: false,
        }
    }
    pub fn push(&mut self, bytes: &[u8]) -> Result<Vec<Event>, FramingError> {
        if self.failed {
            return Err(FramingError("decoder is failed"));
        }
        let result = self.push_inner(bytes);
        if result.is_err() {
            self.failed = true;
        }
        result
    }
    fn push_inner(&mut self, bytes: &[u8]) -> Result<Vec<Event>, FramingError> {
        let mut events = Vec::new();
        for &byte in bytes {
            if self.after_cr {
                self.after_cr = false;
                if byte == b'\n' {
                    self.count_byte()?;
                    self.end_line(&mut events)?;
                    continue;
                }
                self.end_line(&mut events)?;
            }
            self.count_byte()?;
            if byte == b'\r' {
                self.after_cr = true;
            } else if byte == b'\n' {
                self.end_line(&mut events)?;
            } else {
                self.line.push(byte);
            }
        }
        Ok(events)
    }
    fn count_byte(&mut self) -> Result<(), FramingError> {
        if self.frame_bytes >= self.max_bytes {
            return Err(FramingError("SSE frame exceeds byte limit"));
        }
        self.frame_bytes += 1;
        Ok(())
    }
    fn end_line(&mut self, events: &mut Vec<Event>) -> Result<(), FramingError> {
        let line = std::str::from_utf8(&self.line)
            .map_err(|_| FramingError("SSE frame contains invalid UTF-8"))?;
        if line.is_empty() {
            if !self.data.is_empty() {
                events.push(Event {
                    data: self.data.join("\n"),
                    event: self.event.take(),
                });
            }
            self.data.clear();
            self.event = None;
            self.frame_bytes = 0;
        } else if !line.starts_with(':') {
            let (field, value) = line.split_once(':').unwrap_or((line, ""));
            let value = value.strip_prefix(' ').unwrap_or(value);
            match field {
                "data" => self.data.push(value.to_owned()),
                "event" => self.event = Some(value.to_owned()),
                _ => {}
            }
        }
        self.line.clear();
        Ok(())
    }
    pub fn finish(&mut self) -> Result<Vec<Event>, FramingError> {
        if self.failed {
            return Err(FramingError("decoder is failed"));
        }
        let mut events = Vec::new();
        if self.after_cr {
            self.after_cr = false;
            self.end_line(&mut events)?;
        }
        if self.frame_bytes != 0 {
            self.failed = true;
            return Err(FramingError("incomplete SSE frame at EOF"));
        }
        Ok(events)
    }
}
