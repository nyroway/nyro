import { Light as SyntaxHighlighter } from "react-syntax-highlighter";
import { atomOneDark, github } from "react-syntax-highlighter/dist/esm/styles/hljs";
import bash from "react-syntax-highlighter/dist/esm/languages/hljs/bash";
import python from "react-syntax-highlighter/dist/esm/languages/hljs/python";
import typescript from "react-syntax-highlighter/dist/esm/languages/hljs/typescript";

SyntaxHighlighter.registerLanguage("bash", bash);
SyntaxHighlighter.registerLanguage("python", python);
SyntaxHighlighter.registerLanguage("typescript", typescript);

type CodeHighlighterProps = {
  code: string;
  language: string;
  dark?: boolean;
  padding?: string | number;
};

export default function CodeHighlighter({
  code,
  language,
  dark = false,
  padding = 0,
}: CodeHighlighterProps) {
  return (
    <SyntaxHighlighter
      language={language}
      style={dark ? atomOneDark : github}
      customStyle={{ margin: 0, borderRadius: 8, padding, background: "transparent" }}
      codeTagProps={{ style: { fontSize: "13px", lineHeight: "1.55" } }}
      showLineNumbers={false}
      wrapLongLines
    >
      {code}
    </SyntaxHighlighter>
  );
}
