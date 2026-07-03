import { CSSProperties } from "react";
import { cn } from "@/lib/utils";
import nyroIconUrl from "@/assets/icons/nyro.png";

interface NyroIconProps {
  size?: number;
  className?: string;
  monochrome?: boolean;
  title?: string;
}

export function NyroIcon({
  size = 24,
  className,
  monochrome = false,
  title = "Nyro",
}: NyroIconProps) {
  const style: CSSProperties = monochrome
    ? {
        width: size,
        height: size,
        backgroundColor: "currentColor",
        maskImage: `url("${nyroIconUrl}")`,
        maskRepeat: "no-repeat",
        maskPosition: "center",
        maskSize: "contain",
        WebkitMaskImage: `url("${nyroIconUrl}")`,
        WebkitMaskRepeat: "no-repeat",
        WebkitMaskPosition: "center",
        WebkitMaskSize: "contain",
      }
    : {
        width: size,
        height: size,
        backgroundImage: `url("${nyroIconUrl}")`,
        backgroundRepeat: "no-repeat",
        backgroundPosition: "center",
        backgroundSize: "contain",
      };

  return (
    <span
      className={cn("nyro-icon", monochrome && "nyro-icon-mono", className)}
      style={style}
      aria-hidden="true"
      title={title}
    />
  );
}
