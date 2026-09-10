//! Schema expectations use literal wire forms, never Nyro's normalization helpers.
use nyro_llm::{
    codec::{anthropic, gemini, openai},
    ir::ChatRequest,
};
use serde_json::{Value, json};

fn chat(schema: Value, strict: bool) -> Value {
    json!({"model":"m","messages":[{"role":"user","content":"hello"}],"max_tokens":64,
        "tools":[{"type":"function","function":{"name":"lookup","parameters":schema,"strict":strict}}]})
}
fn native(schema: Value) -> Value {
    json!({"contents":[{"parts":[{"text":"hello"}]}],"generationConfig":{"maxOutputTokens":64},
        "tools":[{"functionDeclarations":[{"name":"lookup","parameters":schema}]}]})
}
fn schemas(r: &ChatRequest) -> Vec<Value> {
    vec![openai::encode_chat(r).unwrap()["tools"][0]["function"]["parameters"].clone(),
        openai::responses::encode_chat(r).unwrap()["tools"][0]["parameters"].clone(),
        anthropic::encode_chat(r).unwrap()["tools"][0]["input_schema"].clone(),
        gemini::encode_chat(r).unwrap()["tools"][0]["functionDeclarations"][0]["parametersJsonSchema"].clone()]
}
#[test]
fn explicit_non_strict_tools_are_portable_but_strict_guarantees_are_not_dropped() {
    for strict in [false, true] {
        let r = openai::decode_chat(chat(
            json!({"type":"object","properties":{},"additionalProperties":false}),
            strict,
        ))
        .unwrap();
        assert_eq!(
            openai::encode_chat(&r).unwrap()["tools"][0]["function"]["strict"],
            strict
        );
        assert_eq!(
            openai::responses::encode_chat(&r).unwrap()["tools"][0]["strict"],
            strict
        );
        assert_eq!(anthropic::encode_chat(&r).is_ok(), !strict);
        assert_eq!(gemini::encode_chat(&r).is_ok(), !strict);
    }
}
#[test]
fn function_envelopes_are_checked_at_decode_and_public_encode_boundaries() {
    let valid = chat(json!({"type":"object"}), false);
    for bad_schema in [json!(true), json!([]), json!("object"), json!(3)] {
        let mut value = valid.clone();
        value["tools"][0]["function"]["parameters"] = bad_schema.clone();
        assert!(openai::decode_chat(value).is_err(), "{bad_schema}");
        let mut r = openai::decode_chat(valid.clone()).unwrap();
        let nyro_protocol::openai::chat::Tool::Function { function } =
            &mut r.openai.tools.as_mut().unwrap()[0];
        function.parameters = Some(bad_schema);
        assert!(openai::encode_chat(&r).is_err());
        assert!(openai::responses::encode_chat(&r).is_err());
        assert!(anthropic::encode_chat(&r).is_err());
        assert!(gemini::encode_chat(&r).is_err());
    }
    for name in ["", "   "] {
        let mut value = valid.clone();
        value["tools"][0]["function"]["name"] = json!(name);
        assert!(openai::decode_chat(value).is_err());
    }
}
#[test]
fn json_schema_references_constraints_and_literal_data_are_not_rewritten() {
    let schema = json!({"type":"object","$defs":{"value":{"type":"string","pattern":"^[A-Z]+$"}},
        "properties":{"item":{"$ref":"#/$defs/value"},"optional":{"anyOf":[{"type":"null"},{"type":"integer","minimum":0}]}},
        "required":["item"],"additionalProperties":false,
        "default":{"type":"BUSINESS_DATA","nullable":true,"$ref":"literal"}});
    let mut r = openai::decode_chat(chat(schema.clone(), false)).unwrap();
    // Existing non-strict IR form also exercises preservation independently of false support.
    let nyro_protocol::openai::chat::Tool::Function { function } =
        &mut r.openai.tools.as_mut().unwrap()[0];
    function.strict = None;
    for actual in schemas(&r) {
        assert_eq!(actual, schema);
    }
    let mut v = native(json!({}));
    let f = &mut v["tools"][0]["functionDeclarations"][0];
    f.as_object_mut().unwrap().remove("parameters");
    f["parametersJsonSchema"] = schema.clone();
    for actual in schemas(&gemini::decode_chat(v, "m", false).unwrap()) {
        assert_eq!(actual, schema);
    }
}
#[test]
fn gemini_schema_converts_nested_constraints_without_touching_default_data() {
    let native_schema = json!({"type":"OBJECT","title":"Query","minProperties":"1","maxProperties":4,
        "properties":{
            "items":{"type":"ARRAY","minItems":"1","maxItems":3,"items":{"type":"STRING","minLength":"2","maxLength":"8","pattern":"^[A-Z]+$"}},
            "choice":{"type":"STRING","format":"enum","enum":["A","B"]},
            "value":{"anyOf":[{"type":"INTEGER","minimum":0,"maximum":7},{"type":"STRING","nullable":true}]},
            "data":{"type":"OBJECT","default":{"type":"STRING","nullable":true,"minItems":"business"}}
        },"required":["items"]});
    let expected = json!({"type":"object","title":"Query","minProperties":1,"maxProperties":4,
        "properties":{
            "items":{"type":"array","minItems":1,"maxItems":3,"items":{"type":"string","minLength":2,"maxLength":8,"pattern":"^[A-Z]+$"}},
            "choice":{"type":"string","format":"enum","enum":["A","B"]},
            "value":{"anyOf":[{"type":"integer","minimum":0,"maximum":7},{"type":["string","null"]}]},
            "data":{"type":"object","default":{"type":"STRING","nullable":true,"minItems":"business"}}
        },"required":["items"]});
    for actual in schemas(&gemini::decode_chat(native(native_schema), "m", false).unwrap()) {
        assert_eq!(actual, expected);
    }
}
#[test]
fn gemini_schema_rejects_malformed_or_unmapped_constraints_instead_of_weakening_them() {
    for field in [
        json!({"type":"UNRECOGNIZED"}),
        json!({"type":3}),
        json!({"type":"OBJECT","properties":[]}),
        json!({"type":"OBJECT","required":"x"}),
        json!({"type":"STRING","enum":[1]}),
        json!({"type":"INTEGER","enum":["1"]}),
        json!({"type":"STRING","nullable":true,"enum":["A"]}),
        json!({"type":"STRING","nullable":true,"anyOf":[{"type":"STRING"}]}),
        json!({"type":"STRING","nullable":"true"}),
        json!({"type":"ARRAY","minItems":"1.5"}),
        json!({"type":"ARRAY","maxItems":-1}),
        json!({"type":"ARRAY","maxItems":"9223372036854775808"}),
        json!({"type":"STRING","minLength":null}),
        json!({"type":"NUMBER","minimum":"1"}),
        json!({"type":"STRING","anyOf":[]}),
        json!({"type":"STRING","anyOf":[false]}),
        json!({"type":"OBJECT","propertyOrdering":["x"]}),
        json!({"type":"OBJECT","additionalProperties":false}),
        json!({"type":"OBJECT","$ref":"#/defs/x"}),
    ] {
        assert!(
            gemini::decode_chat(
                native(json!({"type":"OBJECT","properties":{"x":field.clone()}})),
                "m",
                false
            )
            .is_err(),
            "accepted {field}"
        );
    }
}
