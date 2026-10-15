import * as React from "react";
import * as ToggleGroupPrimitive from "@radix-ui/react-toggle-group";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

const toggleGroupVariants = cva(
  "inline-flex items-center justify-center rounded-xl border border-transparent text-sm font-medium transition-[background-color,color,border-color] hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-slate-300 disabled:pointer-events-none disabled:opacity-50 data-[state=on]:bg-muted data-[state=on]:text-foreground",
  {
    variants: {
      variant: {
        default: "bg-transparent",
        outline: "border border-border bg-transparent shadow-none",
      },
      size: {
        default: "h-10 px-3",
        sm: "h-9 px-2.5",
        lg: "h-11 px-5",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  },
);

function ToggleGroup({
  className,
  ...props
}: React.ComponentProps<typeof ToggleGroupPrimitive.Root>) {
  return (
    <ToggleGroupPrimitive.Root
      data-slot="toggle-group"
      className={cn("flex items-center gap-2", className)}
      {...props}
    />
  );
}

function ToggleGroupItem({
  className,
  variant,
  size,
  children,
  ...props
}: React.ComponentProps<typeof ToggleGroupPrimitive.Item> &
  VariantProps<typeof toggleGroupVariants>) {
  return (
    <ToggleGroupPrimitive.Item
      data-slot="toggle-group-item"
      className={cn(toggleGroupVariants({ variant, size }), className)}
      {...props}
    >
      {children}
    </ToggleGroupPrimitive.Item>
  );
}

export { ToggleGroup, ToggleGroupItem, toggleGroupVariants };
