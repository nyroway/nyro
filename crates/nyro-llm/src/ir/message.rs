use super::{Content, Message, ResponseMessage, ToolCall};
use serde::{Deserialize, Serialize};

/// The sole order of a message body. Content retains its nested part boundaries.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(
    tag = "type",
    content = "value",
    rename_all = "snake_case",
    deny_unknown_fields
)]
pub enum MessageItem {
    Content(Content),
    ToolCall(ToolCall),
    ResponsesMessage {
        id: Option<String>,
        status: Option<nyro_protocol::openai::responses::ItemStatus>,
        content: Content,
    },
    ResponsesToolCall {
        id: Option<String>,
        status: Option<nyro_protocol::openai::responses::ItemStatus>,
        call: ToolCall,
    },
}

impl MessageItem {
    pub fn as_content(&self) -> Option<&Content> {
        match self {
            Self::Content(c) | Self::ResponsesMessage { content: c, .. } => Some(c),
            _ => None,
        }
    }
    pub fn as_content_mut(&mut self) -> Option<&mut Content> {
        match self {
            Self::Content(c) | Self::ResponsesMessage { content: c, .. } => Some(c),
            _ => None,
        }
    }
    pub fn as_tool_call(&self) -> Option<&ToolCall> {
        match self {
            Self::ToolCall(c) | Self::ResponsesToolCall { call: c, .. } => Some(c),
            _ => None,
        }
    }
    pub fn as_tool_call_mut(&mut self) -> Option<&mut ToolCall> {
        match self {
            Self::ToolCall(c) | Self::ResponsesToolCall { call: c, .. } => Some(c),
            _ => None,
        }
    }

    /// Builds the content-then-calls subset represented by Chat Completions.
    pub fn from_parts(content: Option<Content>, calls: Option<Vec<ToolCall>>) -> Vec<Self> {
        content
            .into_iter()
            .map(Self::Content)
            .chain(calls.into_iter().flatten().map(Self::ToolCall))
            .collect()
    }
}

macro_rules! item_views {
    ($ty:ty) => {
        impl $ty {
            /// Borrow the first content group. Codecs must validate their supported
            /// ordering before using this projection; arbitrary items stay in `items`.
            pub fn content(&self) -> Option<&Content> {
                self.items.iter().find_map(MessageItem::as_content)
            }
            pub fn content_mut(&mut self) -> Option<&mut Content> {
                self.items.iter_mut().find_map(MessageItem::as_content_mut)
            }
            pub fn tool_calls(&self) -> impl Iterator<Item = &ToolCall> {
                self.items.iter().filter_map(MessageItem::as_tool_call)
            }
            pub fn tool_calls_mut(&mut self) -> impl Iterator<Item = &mut ToolCall> {
                self.items
                    .iter_mut()
                    .filter_map(MessageItem::as_tool_call_mut)
            }
        }
    };
}
item_views!(Message);
item_views!(ResponseMessage);
