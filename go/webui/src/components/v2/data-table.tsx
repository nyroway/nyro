import type { ReactNode } from "react";
import clsx from "clsx";

export type DataTableColumn<T> = {
  key: string;
  header: ReactNode;
  render: (row: T) => ReactNode;
  className?: string;
};

export type DataTableProps<T> = {
  columns: DataTableColumn<T>[];
  rows: T[];
  rowKey: (row: T) => string;
  loading?: boolean;
  empty: ReactNode;
  onRowClick?: (row: T) => void;
  className?: string;
  /** Toolbar content: rendered in .table-toolbar above the in-card table (baseline table-card shape). */
  toolbar?: ReactNode;
  /** Footer content: rendered in .table-footer below the table (counts/pagination, etc.). */
  footer?: ReactNode;
  /** Full-card shape: the root node emits card table-card (toolbar + table + footer in one card). */
  carded?: boolean;
};

export function DataTable<T>({
  columns,
  rows,
  rowKey,
  loading = false,
  empty,
  onRowClick,
  className,
  toolbar,
  footer,
  carded = false,
}: DataTableProps<T>) {
  const isEmpty = !loading && rows.length === 0;
  return (
    <div className={clsx(carded && "card", "table-card", isEmpty && "is-empty", className)}>
      {toolbar && <div className="table-toolbar">{toolbar}</div>}
      <div className="table-scroll">
        <table className="table">
          <thead>
            <tr>{columns.map((column) => <th className={column.className} key={column.key}>{column.header}</th>)}</tr>
          </thead>
          <tbody>
            {loading ? (
              Array.from({ length: 4 }, (_, index) => (
                <tr key={`loading-${index}`}>
                  <td colSpan={columns.length}><div className="skeleton" /></td>
                </tr>
              ))
            ) : rows.map((row) => (
              <tr
                key={rowKey(row)}
                className={onRowClick ? "clickable" : undefined}
                onClick={onRowClick ? () => onRowClick(row) : undefined}
                tabIndex={onRowClick ? 0 : undefined}
                onKeyDown={onRowClick ? (event) => {
                  if (event.key === "Enter" || event.key === " ") onRowClick(row);
                } : undefined}
              >
                {columns.map((column) => <td className={column.className} key={column.key}>{column.render(row)}</td>)}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="table-empty">{empty}</div>
      {footer && <footer className="table-footer">{footer}</footer>}
    </div>
  );
}
