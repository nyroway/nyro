//! Image envelope conversion only: no fetching, image decoding or transcoding.
use super::CodecError;
use crate::ir::ImageUrl;
use base64::{Engine, engine::general_purpose::STANDARD};

pub(super) enum Source<'a> {
    Url(&'a str),
    Base64 { mime: &'a str, data: &'a str },
}

pub(super) fn source(image: &ImageUrl) -> Result<Source<'_>, CodecError> {
    let bad = || CodecError("unsupported or invalid image source/detail".into());
    if image
        .detail
        .as_deref()
        .is_some_and(|d| !matches!(d, "auto" | "low" | "high" | "original"))
    {
        return Err(bad());
    }
    if let Some(value) = image.url.strip_prefix("data:") {
        let (mime, data) = value.split_once(";base64,").ok_or_else(bad)?;
        if !matches!(
            mime,
            "image/png" | "image/jpeg" | "image/webp" | "image/gif"
        ) || data.is_empty()
        {
            return Err(bad());
        }
        STANDARD.decode(data).map_err(|_| bad())?;
        Ok(Source::Base64 { mime, data })
    } else {
        let url = reqwest::Url::parse(&image.url).map_err(|_| bad())?;
        if !matches!(url.scheme(), "http" | "https") || url.host_str().is_none() {
            return Err(bad());
        }
        Ok(Source::Url(&image.url))
    }
}

pub(super) fn default_detail(image: &ImageUrl) -> Result<(), CodecError> {
    if image.detail.as_deref().is_some_and(|d| d != "auto") {
        Err(CodecError(
            "image detail cannot be represented by this destination".into(),
        ))
    } else {
        Ok(())
    }
}
