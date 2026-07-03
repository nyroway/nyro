"use client";

import * as React from "react";
import { Check, ChevronsUpDown, X } from "lucide-react";

import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Command, CommandEmpty, CommandGroup,
  CommandInput, CommandItem, CommandList,
} from "@/components/ui/command";
import {
  Popover, PopoverContent, PopoverTrigger,
} from "@/components/ui/popover";

type Option = { value: string; label: string };

interface MultiSelectProps {
  options: Option[];
  values?: string[];
  placeholder?: string;
  searchPlaceholder?: string;
  emptyText?: string;
  onChange?: (values: string[]) => void;
  className?: string;
}

export function MultiSelect({
  options,
  values,
  placeholder = "选择...",
  searchPlaceholder = "搜索...",
  emptyText = "无匹配结果",
  onChange,
  className,
}: MultiSelectProps) {
  const [open, setOpen] = React.useState(false);
  const [internalSelected, setInternalSelected] = React.useState<string[]>(values ?? []);
  const selected = values ?? internalSelected;

  const toggle = (value: string) => {
    const next = selected.includes(value)
      ? selected.filter(v => v !== value)
      : [...selected, value];
    if (values === undefined) setInternalSelected(next);
    onChange?.(next);
  };

  const remove = (value: string) => {
    const next = selected.filter(v => v !== value);
    if (values === undefined) setInternalSelected(next);
    onChange?.(next);
  };

  React.useEffect(() => {
    if (values !== undefined) {
      setInternalSelected(values);
    }
  }, [values]);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          role="combobox"
          className={cn("w-full justify-start h-auto min-h-10 flex-wrap gap-1", className)}
        >
          {selected.length === 0 ? (
            <span className="text-muted-foreground">{placeholder}</span>
          ) : (
            selected.map(val => {
              const label = options.find(o => o.value === val)?.label
              return (
                <Badge key={val} variant="secondary" className="gap-1">
                  {label}
                  <X
                    className="h-3 w-3 cursor-pointer"
                    onClick={e => { e.stopPropagation(); remove(val) }}
                  />
                </Badge>
              )
            })
          )}
          <ChevronsUpDown className="ml-auto h-4 w-4 shrink-0 opacity-50" />
        </Button>
      </PopoverTrigger>

      <PopoverContent className="nyro-shadcn-select-content w-[var(--radix-popover-trigger-width)] p-0">
        <Command>
          <CommandInput placeholder={searchPlaceholder} />
          <CommandList>
            <CommandEmpty>{emptyText}</CommandEmpty>
            <CommandGroup>
              {options.map(option => (
                <CommandItem
                  key={option.value}
                  value={`${option.label} ${option.value}`}
                  onSelect={() => toggle(option.value)}
                >
                  <Check className={cn(
                    "mr-2 h-4 w-4",
                    selected.includes(option.value) ? "opacity-100" : "opacity-0"
                  )} />
                  {option.label}
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}