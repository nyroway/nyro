import { createContext, useContext, useEffect, useMemo, useState } from "react";

export type Locale = "zh-CN" | "en-US";

interface LocaleContextValue {
  locale: Locale;
  setLocale: (next: Locale) => void;
}

const LocaleContext = createContext<LocaleContextValue | null>(null);

export function LocaleProvider({ children }: { children: React.ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>("en-US");

  useEffect(() => {
    const saved = localStorage.getItem("nyro-locale");
    const initial: Locale =
      saved === "zh-CN" || saved === "en-US"
        ? saved
        : navigator.language.startsWith("zh")
          ? "zh-CN"
          : "en-US";
    setLocaleState(initial);
    document.documentElement.lang = initial;
  }, []);

  const setLocale = (next: Locale) => {
    setLocaleState(next);
    localStorage.setItem("nyro-locale", next);
    document.documentElement.lang = next;
  };

  const value = useMemo(() => ({ locale, setLocale }), [locale]);

  return <LocaleContext.Provider value={value}>{children}</LocaleContext.Provider>;
}

export function useLocale() {
  const ctx = useContext(LocaleContext);
  if (!ctx) {
    throw new Error("useLocale must be used within LocaleProvider");
  }
  return ctx;
}
