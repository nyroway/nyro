import { useMemo, useState } from "react";
import { Check, ChevronsUpDown } from "lucide-react";

import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";

export type ComboboxOption = {
  value: string;
  label: string;
};

type ComboboxProps = {
  options: ComboboxOption[];
  value?: string;
  placeholder?: string;
  searchPlaceholder?: string;
  emptyText?: string;
  onValueChange: (value: string) => void;
  className?: string;
  allowCustom?: boolean;
  formatCustomOption?: (value: string) => string;
};

export function Combobox({
  options,
  value,
  placeholder = "Select...",
  searchPlaceholder = "Search...",
  emptyText = "No options",
  onValueChange,
  className,
  allowCustom = false,
  formatCustomOption,
}: ComboboxProps) {
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const selected = useMemo(
    () => options.find((option) => option.value === value),
    [options, value],
  );

  const trimmedSearch = search.trim();
  const matchesExistingOption = useMemo(
    () =>
      options.some(
        (option) => option.value === trimmedSearch || option.label === trimmedSearch,
      ),
    [options, trimmedSearch],
  );
  const showCustomOption = allowCustom && trimmedSearch !== "" && !matchesExistingOption;
  const customOptionLabel =
    formatCustomOption?.(trimmedSearch) ?? `Use "${trimmedSearch}"`;

  return (
    <Popover
      open={open}
      onOpenChange={(nextOpen) => {
        setOpen(nextOpen);
        if (!nextOpen) {
          setSearch("");
        }
      }}
    >
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          role="combobox"
          aria-expanded={open}
          className={cn("h-10 w-full justify-between rounded-md px-3 font-normal", className)}
        >
          <span className="truncate text-left">
            {selected?.label ?? (value ? value : placeholder)}
          </span>
          <ChevronsUpDown className="ml-2 h-4 w-4 shrink-0 opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="nyro-shadcn-select-content w-[var(--radix-popover-trigger-width)] p-0">
        <Command>
          <CommandInput
            placeholder={searchPlaceholder}
            value={search}
            onValueChange={setSearch}
          />
          <CommandList>
            {!showCustomOption && <CommandEmpty>{emptyText}</CommandEmpty>}
            <CommandGroup>
              {options.map((option) => (
                <CommandItem
                  key={option.value}
                  value={option.label}
                  onSelect={() => {
                    onValueChange(option.value);
                    setOpen(false);
                  }}
                >
                  <Check
                    className={cn(
                      "h-4 w-4",
                      value === option.value ? "opacity-100" : "opacity-0",
                    )}
                  />
                  <span className="truncate">{option.label}</span>
                </CommandItem>
              ))}
              {showCustomOption && (
                <CommandItem
                  key="__combobox_custom_value__"
                  value={`__combobox_custom_value__${trimmedSearch}`}
                  onSelect={() => {
                    onValueChange(trimmedSearch);
                    setOpen(false);
                  }}
                >
                  <Check className="h-4 w-4 opacity-0" />
                  <span className="truncate">{customOptionLabel}</span>
                </CommandItem>
              )}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
