import * as React from "react";

import { cn } from "@/lib/utils";

function Input({
  className,
  type,
  autoCapitalize,
  autoCorrect,
  spellCheck,
  autoComplete,
  ...props
}: React.ComponentProps<"input">) {
  return (
    <input
      type={type}
      data-slot="input"
      autoCapitalize={autoCapitalize ?? "none"}
      autoCorrect={autoCorrect ?? "off"}
      spellCheck={spellCheck ?? false}
      autoComplete={autoComplete ?? "off"}
      className={cn(
        "nyro-shadcn-input flex h-10 w-full rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground transition-[border-color,background-color,color] outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-slate-300 disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    />
  );
}

export { Input };
