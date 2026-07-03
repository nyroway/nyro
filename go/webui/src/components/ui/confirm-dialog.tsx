import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

type ConfirmDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: string;
  content?: React.ReactNode;
  hideCancel?: boolean;
  cancelText?: string;
  confirmText?: string;
  onConfirm: () => void;
  confirmClassName?: string;
};

function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  content,
  hideCancel = false,
  cancelText = "Cancel",
  confirmText = "Confirm",
  onConfirm,
  confirmClassName,
}: ConfirmDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description && <DialogDescription>{description}</DialogDescription>}
        </DialogHeader>
        {content && <div className="py-2">{content}</div>}
        <DialogFooter>
          {!hideCancel && (
            <Button variant="secondary" onClick={() => onOpenChange(false)}>
              {cancelText}
            </Button>
          )}
          <Button
            onClick={onConfirm}
            className={cn("bg-red-600 text-white hover:bg-red-500", confirmClassName)}
          >
            {confirmText}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export { ConfirmDialog };
