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
  /** 工具条内容：渲染在卡内表格上方的 .table-toolbar（基线 table-card 形态）。 */
  toolbar?: ReactNode;
  /** 页脚内容：渲染在表格下方的 .table-footer（计数/分页等）。 */
  footer?: ReactNode;
  /** 整卡形态：根节点发 card table-card（工具条 + 表格 + 页脚同一张卡）。 */
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
