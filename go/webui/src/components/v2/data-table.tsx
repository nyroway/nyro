import type { ReactNode } from "react";

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
};

export function DataTable<T>({ columns, rows, rowKey, loading = false, empty, onRowClick, className = "" }: DataTableProps<T>) {
  return (
    <div className={`v2-table-wrap ${className}`.trim()}>
      <table className="v2-data-table">
        <thead>
          <tr>{columns.map((column) => <th className={column.className} key={column.key}>{column.header}</th>)}</tr>
        </thead>
        <tbody>
          {loading ? (
            Array.from({ length: 4 }, (_, index) => (
              <tr className="v2-table-skeleton" key={`loading-${index}`}>
                <td colSpan={columns.length}><span /></td>
              </tr>
            ))
          ) : rows.length === 0 ? (
            <tr className="v2-table-empty"><td colSpan={columns.length}>{empty}</td></tr>
          ) : rows.map((row) => (
            <tr
              key={rowKey(row)}
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
  );
}
