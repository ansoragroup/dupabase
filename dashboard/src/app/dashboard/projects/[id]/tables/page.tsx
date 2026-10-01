"use client";

import { deferEffect } from "@/lib/defer-effect";

import { useEffect, useState, useCallback, use } from "react";
import { useAuth } from "@/lib/auth-context";
import {
  tables as tablesApi,
  type TableInfo,
  type ColumnInfo,
  type TableRowsResponse,
} from "@/lib/api";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { toast } from "sonner";
import {
  ArrowLeft,
  Database,
  Plus,
  Columns3,
  Rows3,
  RefreshCw,
} from "lucide-react";
import Link from "next/link";
import { DataGrid } from "@/components/dashboard/data-grid";

export default function TableBrowserPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  const { token } = useAuth();
  const [tableList, setTableList] = useState<TableInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [selectedTable, setSelectedTable] = useState<string | null>(null);
  const [selectedSchema, setSelectedSchema] = useState("public");
  const [columns, setColumns] = useState<ColumnInfo[]>([]);
  const primaryColumns = columns.filter((column) => column.primary_key);
  const identityColumn = primaryColumns.length === 1 && primaryColumns[0].unique
    ? primaryColumns[0] : columns.find((column) => column.unique);
  const [rowsData, setRowsData] = useState<TableRowsResponse | null>(null);
  const [columnsLoading, setColumnsLoading] = useState(false);
  const [rowsLoading, setRowsLoading] = useState(false);
  const [page, setPage] = useState(1);
  const [insertOpen, setInsertOpen] = useState(false);
  const [insertValues, setInsertValues] = useState<Record<string, string>>({});
  const [insertLoading, setInsertLoading] = useState(false);

  const loadTables = useCallback(async () => {
    if (!token) return;
    const { data, error } = await tablesApi.list(token, id);
    if (error) {
      toast.error(error);
    } else if (data) {
      setTableList(data);
    }
    setLoading(false);
  }, [token, id]);

  useEffect(() => deferEffect(() => {
    loadTables();
  }), [loadTables]);

  const loadColumns = useCallback(
    async (table: string, schema: string) => {
      if (!token) return;
      setColumnsLoading(true);
      const { data, error } = await tablesApi.columns(token, id, table, schema);
      if (error) {
        toast.error(error);
      } else if (data) {
        setColumns(data);
      }
      setColumnsLoading(false);
    },
    [token, id]
  );

  const loadRows = useCallback(
    async (table: string, schema: string, p: number) => {
      if (!token) return;
      setRowsLoading(true);
      const { data, error } = await tablesApi.rows(token, id, table, {
        schema,
        page: p,
        perPage: 50,
      });
      if (error) {
        toast.error(error);
      } else if (data) {
        setRowsData(data);
      }
      setRowsLoading(false);
    },
    [token, id]
  );

  const selectTable = (table: string, schema: string) => {
    setSelectedTable(table);
    setSelectedSchema(schema);
    setPage(1);
    loadColumns(table, schema);
    loadRows(table, schema, 1);
  };

  const handlePageChange = (newPage: number) => {
    if (!selectedTable) return;
    setPage(newPage);
    loadRows(selectedTable, selectedSchema, newPage);
  };

  const handleInsert = async () => {
    if (!token || !selectedTable) return;
    setInsertLoading(true);
    const data: Record<string, unknown> = {};
    for (const [key, value] of Object.entries(insertValues)) {
      if (value !== "") data[key] = value;
    }
    const { error } = await tablesApi.insertRow(
      token,
      id,
      selectedTable,
      data,
      selectedSchema
    );
    if (error) {
      toast.error(error);
    } else {
      toast.success("Row inserted");
      setInsertOpen(false);
      setInsertValues({});
      loadRows(selectedTable, selectedSchema, page);
    }
    setInsertLoading(false);
  };

  const handleDeleteRow = async (rowIdx: number) => {
    if (!token || !selectedTable || !rowsData || columns.length === 0) return;
    if (!identityColumn) { toast.error("This table has no supported unique row identity"); return; }
    const pkColumn = identityColumn.name;
    const colIdx = rowsData.columns.indexOf(pkColumn);
    if (colIdx === -1) {
      toast.error("Could not determine primary key column");
      return;
    }
    if (rowsData.rows[rowIdx][colIdx] == null) { toast.error("This row has no unique identity value"); return; }
    const pkValue = String(rowsData.rows[rowIdx][colIdx]);
    const { error } = await tablesApi.deleteRow(
      token,
      id,
      selectedTable,
      pkColumn,
      pkValue,
      selectedSchema
    );
    if (error) {
      toast.error(error);
    } else {
      toast.success("Row deleted");
      loadRows(selectedTable, selectedSchema, page);
    }
  };

  const handleCellEdit = async (
    rowIdx: number,
    colIdx: number,
    _oldValue: unknown,
    newValue: string
  ) => {
    if (!token || !selectedTable || !rowsData || columns.length === 0) return;
    if (!identityColumn) { toast.error("This table has no supported unique row identity"); return; }
    const pkColumn = identityColumn.name;
    const pkColIdx = rowsData.columns.indexOf(pkColumn);
    if (pkColIdx === -1) {
      toast.error("Could not determine primary key column");
      return;
    }
    if (rowsData.rows[rowIdx][pkColIdx] == null) { toast.error("This row has no unique identity value"); return; }
    const pkValue = String(rowsData.rows[rowIdx][pkColIdx]);
    const colName = rowsData.columns[colIdx];
    const { error } = await tablesApi.updateRow(
      token,
      id,
      selectedTable,
      pkColumn,
      pkValue,
      { [colName]: newValue === "" ? null : newValue },
      selectedSchema
    );
    if (error) {
      toast.error(error);
    } else {
      toast.success("Cell updated");
      loadRows(selectedTable, selectedSchema, page);
    }
  };

  const groupedTables = tableList.reduce<Record<string, TableInfo[]>>(
    (acc, t) => {
      if (!acc[t.schema]) acc[t.schema] = [];
      acc[t.schema].push(t);
      return acc;
    },
    {}
  );

  if (loading) {
    return (
      <div className="space-y-6">
        <div className="flex items-center gap-3">
          <Skeleton className="h-9 w-9 rounded-md" />
          <Skeleton className="h-7 w-48" />
        </div>
        <div className="grid grid-cols-[240px_1fr] gap-4">
          <Card>
            <CardContent className="p-4 space-y-2">
              {[1, 2, 3, 4, 5].map((i) => (
                <Skeleton key={i} className="h-8 w-full" />
              ))}
            </CardContent>
          </Card>
          <Card>
            <CardContent className="p-4">
              <Skeleton className="h-48 w-full" />
            </CardContent>
          </Card>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Button variant="ghost" size="icon" className="shrink-0" asChild>
            <Link href={`/dashboard/projects/${id}`}>
              <ArrowLeft className="h-4 w-4" />
            </Link>
          </Button>
          <h2 className="text-xl sm:text-2xl font-bold tracking-tight">
            Table Browser
          </h2>
        </div>
        <Button variant="outline" size="sm" onClick={loadTables}>
          <RefreshCw className="mr-1.5 h-3.5 w-3.5" />
          Refresh
        </Button>
      </div>

      <div className="grid grid-cols-[240px_1fr] gap-4 items-start">
        {/* Sidebar - table list */}
        <Card className="sticky top-4">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm">Tables</CardTitle>
            <CardDescription className="text-xs">
              {tableList.length} table{tableList.length !== 1 ? "s" : ""}
            </CardDescription>
          </CardHeader>
          <CardContent className="p-2">
            {Object.entries(groupedTables).map(([schema, schemaTables]) => (
              <div key={schema} className="mb-2">
                <p className="px-2 py-1 text-xs font-medium text-muted-foreground uppercase tracking-wider">
                  {schema}
                </p>
                {schemaTables.map((t) => (
                  <button
                    key={`${t.schema}.${t.name}`}
                    className={`w-full text-left px-2 py-1.5 rounded-md text-sm transition-colors flex items-center justify-between ${
                      selectedTable === t.name && selectedSchema === t.schema
                        ? "bg-primary text-primary-foreground"
                        : "hover:bg-muted"
                    }`}
                    onClick={() => selectTable(t.name, t.schema)}
                  >
                    <span className="font-mono text-xs truncate">{t.name}</span>
                    <Badge variant="secondary" className="text-[10px] ml-1 shrink-0">
                      {t.column_count}
                    </Badge>
                  </button>
                ))}
              </div>
            ))}
            {tableList.length === 0 && (
              <p className="text-xs text-muted-foreground text-center py-4">
                No tables found
              </p>
            )}
          </CardContent>
        </Card>

        {/* Main content */}
        <div>
          {!selectedTable ? (
            <Card>
              <CardContent className="py-16 text-center text-muted-foreground">
                <Database className="h-8 w-8 mx-auto mb-3 opacity-50" />
                <p>Select a table from the sidebar to browse its data</p>
              </CardContent>
            </Card>
          ) : (
            <Tabs defaultValue="rows">
              <div className="flex items-center justify-between mb-4">
                <TabsList>
                  <TabsTrigger value="columns">
                    <Columns3 className="mr-1.5 h-3.5 w-3.5" />
                    Columns
                  </TabsTrigger>
                  <TabsTrigger value="rows">
                    <Rows3 className="mr-1.5 h-3.5 w-3.5" />
                    Rows
                  </TabsTrigger>
                </TabsList>
                <div className="flex items-center gap-2">
                  <Badge variant="outline" className="font-mono text-xs">
                    {selectedSchema}.{selectedTable}
                  </Badge>
                  <Dialog open={insertOpen} onOpenChange={setInsertOpen}>
                    <DialogTrigger asChild>
                      <Button
                        size="sm"
                        onClick={() => {
                          setInsertValues({});
                          setInsertOpen(true);
                        }}
                      >
                        <Plus className="mr-1.5 h-3.5 w-3.5" />
                        Insert Row
                      </Button>
                    </DialogTrigger>
                    <DialogContent className="max-h-[80vh] overflow-y-auto">
                      <DialogHeader>
                        <DialogTitle>Insert Row</DialogTitle>
                        <DialogDescription>
                          Add a new row to {selectedSchema}.{selectedTable}
                        </DialogDescription>
                      </DialogHeader>
                      <div className="space-y-3">
                        {columns.map((col) => (
                          <div key={col.name} className="space-y-1">
                            <Label className="text-xs">
                              {col.name}{" "}
                              <span className="text-muted-foreground font-normal">
                                ({col.type})
                              </span>
                              {col.nullable && (
                                <span className="text-muted-foreground font-normal">
                                  {" "}
                                  - nullable
                                </span>
                              )}
                            </Label>
                            <Input
                              placeholder={
                                col.default ? `Default: ${col.default}` : ""
                              }
                              value={insertValues[col.name] || ""}
                              onChange={(e) =>
                                setInsertValues((v) => ({
                                  ...v,
                                  [col.name]: e.target.value,
                                }))
                              }
                            />
                          </div>
                        ))}
                      </div>
                      <DialogFooter>
                        <Button
                          variant="outline"
                          onClick={() => setInsertOpen(false)}
                        >
                          Cancel
                        </Button>
                        <Button onClick={handleInsert} disabled={insertLoading}>
                          {insertLoading ? "Inserting..." : "Insert"}
                        </Button>
                      </DialogFooter>
                    </DialogContent>
                  </Dialog>
                </div>
              </div>

              {/* Columns Tab */}
              <TabsContent value="columns">
                <Card>
                  <CardContent className="p-0">
                    {columnsLoading ? (
                      <div className="p-4 space-y-2">
                        {[1, 2, 3].map((i) => (
                          <Skeleton key={i} className="h-8 w-full" />
                        ))}
                      </div>
                    ) : (
                      <Table>
                        <TableHeader>
                          <TableRow>
                            <TableHead>Name</TableHead>
                            <TableHead>Type</TableHead>
                            <TableHead>Nullable</TableHead>
                            <TableHead>Default</TableHead>
                          </TableRow>
                        </TableHeader>
                        <TableBody>
                          {columns.map((col) => (
                            <TableRow key={col.name}>
                              <TableCell className="font-mono text-xs font-medium">
                                {col.name}
                              </TableCell>
                              <TableCell className="font-mono text-xs">
                                {col.type}
                                {col.max_length !== null && `(${col.max_length})`}
                                {col.precision !== null && `(${col.precision})`}
                              </TableCell>
                              <TableCell>
                                <Badge
                                  variant={col.nullable ? "secondary" : "outline"}
                                  className="text-[10px]"
                                >
                                  {col.nullable ? "YES" : "NO"}
                                </Badge>
                              </TableCell>
                              <TableCell className="font-mono text-xs text-muted-foreground">
                                {col.default || "\u2014"}
                              </TableCell>
                            </TableRow>
                          ))}
                          {columns.length === 0 && (
                            <TableRow>
                              <TableCell
                                colSpan={4}
                                className="text-center text-muted-foreground py-8"
                              >
                                No columns found
                              </TableCell>
                            </TableRow>
                          )}
                        </TableBody>
                      </Table>
                    )}
                  </CardContent>
                </Card>
              </TabsContent>

              {/* Rows Tab */}
              <TabsContent value="rows">
                <Card>
                  <CardContent className="p-0">
                    {rowsData ? (
                      <DataGrid
                        columns={rowsData.columns}
                        rows={rowsData.rows}
                        total={rowsData.total}
                        page={page}
                        perPage={rowsData.per_page}
                        loading={rowsLoading}
                        onPageChange={handlePageChange}
                        onCellEdit={identityColumn ? handleCellEdit : undefined}
                        onDeleteRow={identityColumn ? handleDeleteRow : undefined}
                        emptyMessage="No rows found"
                      />
                    ) : rowsLoading ? (
                      <div className="p-4 space-y-2">
                        {[1, 2, 3, 4, 5].map((i) => (
                          <Skeleton key={i} className="h-8 w-full" />
                        ))}
                      </div>
                    ) : (
                      <div className="p-8 text-center text-muted-foreground text-sm">
                        No data loaded
                      </div>
                    )}
                  </CardContent>
                </Card>
              </TabsContent>
            </Tabs>
          )}
        </div>
      </div>
    </div>
  );
}
