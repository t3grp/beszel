import { RefreshCwIcon, Trash2Icon } from "lucide-react"
import { useCallback, useEffect, useRef, useState } from "react"
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Checkbox } from "@/components/ui/checkbox"
import { isAdmin, pb } from "@/lib/api"
import { cn, decimalString, formatBytes, formatShortDate } from "@/lib/utils"
import type { DiskBreakdown, DiskCategory, DiskItem, DiskPruneRequest } from "@/types"

/** How often to ask the agent again while it is still working. */
const POLL_MS = 3000
/** Stop polling after this long; the agent gives up on a prune and on a scan after 10 minutes each. */
const POLL_MAX_MS = 21 * 60 * 1000

function bytes(size: number) {
	const { value, unit } = formatBytes(size)
	return `${decimalString(value, value >= 10 ? 1 : 2)} ${unit}`
}

const dockerCategories: { key: "images" | "containers" | "volumes" | "buildCache"; label: string; color: string }[] = [
	{ key: "images", label: "Images", color: "var(--chart-1)" },
	{ key: "containers", label: "Container layers", color: "var(--chart-2)" },
	{ key: "volumes", label: "Volumes", color: "var(--chart-3)" },
	{ key: "buildCache", label: "Build cache", color: "var(--chart-4)" },
]

/** What the prune dialog offers. Volumes are never offered. Stopped containers start unticked. */
const pruneOptions: {
	key: keyof DiskPruneRequest
	label: string
	hint: string
	docker: "images" | "containers" | "buildCache"
}[] = [
	{ key: "images", label: "Unused images", hint: "Images no container uses", docker: "images" },
	{ key: "buildCache", label: "Build cache", hint: "Unused build cache entries", docker: "buildCache" },
	{
		key: "containers",
		label: "Stopped containers",
		hint: "A stopped service is rebuilt on its next deploy",
		docker: "containers",
	},
]

const kindLabel: Record<string, string> = {
	image: "Image",
	container: "Container",
	volume: "Volume",
	dir: "Folder",
	file: "File",
}

/**
 * What is using disk space on a system: the Docker engine's own accounting plus the
 * folders the agent was told to scan. The agent scans in the background when asked,
 * so this polls until the scan has finished.
 */
export default function DiskBreakdownCard({ systemId, canPrune }: { systemId: string; canPrune: boolean }) {
	const [data, setData] = useState<DiskBreakdown | null>(null)
	const [error, setError] = useState<string | null>(null)
	const [pruneOpen, setPruneOpen] = useState(false)
	const [pruneError, setPruneError] = useState<string | null>(null)
	const [selection, setSelection] = useState<DiskPruneRequest>({ images: true, buildCache: true, containers: false })
	const timer = useRef<ReturnType<typeof setTimeout>>(undefined)
	const startedAt = useRef(0)
	const cancelled = useRef(false)

	const load = useCallback(
		(refresh: boolean) => {
			if (refresh) startedAt.current = Date.now()
			const request = refresh
				? pb.send<DiskBreakdown>("/api/beszel/disk-breakdown/refresh", { method: "POST", query: { system: systemId } })
				: pb.send<DiskBreakdown>("/api/beszel/disk-breakdown", { query: { system: systemId } })
			request
				.then((result) => {
					if (cancelled.current) return
					setData(result)
					setError(null)
					const working = result.refreshing || result.pruning || !result.checkedAt
					if (working && Date.now() - startedAt.current < POLL_MAX_MS) {
						clearTimeout(timer.current)
						timer.current = setTimeout(() => load(false), POLL_MS)
					}
				})
				.catch((err) => {
					if (cancelled.current) return
					setError(err?.message || "Failed to load disk breakdown")
				})
		},
		[systemId]
	)

	useEffect(() => {
		cancelled.current = false
		startedAt.current = Date.now()
		load(false)
		return () => {
			cancelled.current = true
			clearTimeout(timer.current)
		}
	}, [load])

	/** Starts the prune on the agent, then polls until it and the scan after it finish. */
	const prune = useCallback(() => {
		setPruneError(null)
		startedAt.current = Date.now()
		pb.send<DiskBreakdown>("/api/beszel/disk-breakdown/prune", {
			method: "POST",
			query: { system: systemId },
			body: selection,
		})
			.then((result) => {
				if (cancelled.current) return
				setData(result)
				clearTimeout(timer.current)
				timer.current = setTimeout(() => load(false), POLL_MS)
			})
			.catch((err) => {
				if (cancelled.current) return
				setPruneError(err?.message || "Failed to free space")
			})
	}, [systemId, selection, load])

	if (!data && !error) {
		return null
	}

	const pruning = !!data?.pruning
	const scanning = !!data && (data.refreshing || !data.checkedAt)
	const busy = scanning || pruning
	const docker = data?.docker
	const dockerTotal = docker ? dockerCategories.reduce((sum, c) => sum + docker[c.key].total, 0) : 0

	return (
		<Card className="@container w-full px-3 py-5 sm:py-6 sm:px-6">
			<CardHeader className="p-0 mb-3 sm:mb-4">
				<div className="flex items-end gap-3 w-full">
					<div className="px-2 sm:px-1">
						<CardTitle className="mb-2">Disk Breakdown</CardTitle>
						<CardDescription>
							{pruning
								? "Freeing space..."
								: scanning
									? "Scanning..."
									: data?.checkedAt
										? `Scanned ${formatShortDate(new Date(data.checkedAt * 1000).toISOString())}`
										: ""}
						</CardDescription>
					</div>
					<Button
						type="button"
						variant="outline"
						size="sm"
						className="ms-auto"
						disabled={busy}
						onClick={() => load(true)}
					>
						<RefreshCwIcon className={cn("size-3.5 me-1.5", busy && "animate-spin")} />
						Rescan
					</Button>
				</div>
			</CardHeader>

			{error && <p className="px-2 sm:px-1 text-sm text-destructive">{error}</p>}
			{data?.dockerErr && <p className="px-2 sm:px-1 text-sm text-destructive mb-3">Docker: {data.dockerErr}</p>}

			{docker && (
				<section className="px-2 sm:px-1 mb-6">
					<h4 className="text-sm font-medium mb-2">Docker</h4>
					<div className="flex h-3 w-full overflow-hidden rounded-full bg-muted">
						{dockerTotal > 0 &&
							dockerCategories.map(({ key, color }) => (
								<div
									key={key}
									style={{ width: `${(docker[key].total / dockerTotal) * 100}%`, backgroundColor: color }}
									title={`${bytes(docker[key].total)}`}
								/>
							))}
					</div>
					<div className="mt-3 grid grid-cols-2 @3xl:grid-cols-4 gap-x-4 gap-y-3">
						{dockerCategories.map(({ key, label, color }) => (
							<CategoryStat key={key} label={label} color={color} category={docker[key]} />
						))}
					</div>
					{!!docker.items?.length && <ItemList items={docker.items} className="mt-5" showKind />}
				</section>
			)}

			{data?.paths?.map((path) => (
				<section key={path.path} className="px-2 sm:px-1 mb-6 last:mb-0">
					<div className="flex items-baseline gap-2 mb-2">
						<h4 className="text-sm font-medium font-mono break-all">{path.path}</h4>
						{!path.error && <span className="text-sm text-muted-foreground">{bytes(path.total)}</span>}
						{path.partial && (
							<Badge variant="outline" title="Some entries could not be read, so sizes may be low">
								Partial
							</Badge>
						)}
					</div>
					{path.error ? (
						<p className="text-sm text-destructive">{path.error}</p>
					) : (
						<ItemList items={path.children ?? []} />
					)}
				</section>
			))}

			{isAdmin() && docker && (
				<footer className="px-2 sm:px-1 mt-6 pt-4 border-t flex flex-wrap items-center gap-x-4 gap-y-2">
					<Button
						type="button"
						variant="outline"
						size="sm"
						disabled={!canPrune || busy}
						title={canPrune ? undefined : "Set DISK_PRUNE=true on this system's agent to turn this on"}
						onClick={() => setPruneOpen(true)}
					>
						<Trash2Icon className="size-3.5 me-1.5" />
						Free up space
					</Button>
					<div className="text-sm text-muted-foreground min-w-0">
						{!canPrune ? (
							<>
								Turned off. Set <code className="font-mono">DISK_PRUNE=true</code> on this system's agent to use it.
							</>
						) : pruning ? (
							"Freeing space..."
						) : data?.prune ? (
							<>
								Freed {bytes(data.prune.freed)} ·{" "}
								{formatShortDate(new Date(data.prune.finishedAt * 1000).toISOString())}
							</>
						) : (
							"Removes unused Docker data. Volumes are never touched."
						)}
					</div>
					{pruneError && <p className="basis-full text-sm text-destructive">{pruneError}</p>}
					{!!data?.prune?.errors?.length && (
						<ul className="basis-full text-sm text-destructive">
							{data.prune.errors.map((message) => (
								<li key={message}>{message}</li>
							))}
						</ul>
					)}
				</footer>
			)}

			<AlertDialog open={pruneOpen} onOpenChange={setPruneOpen}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Free up space on this system?</AlertDialogTitle>
						<AlertDialogDescription>
							Removes unused Docker data that is at least 7 days old. Volumes are never touched. Removed images are
							pulled or rebuilt the next time something needs them. This cannot be undone.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<div className="grid gap-3">
						{pruneOptions.map(({ key, label, hint, docker: category }) => (
							<label key={key} htmlFor={`prune-${key}`} className="flex items-start gap-3 cursor-pointer">
								<Checkbox
									id={`prune-${key}`}
									className="mt-0.5"
									checked={selection[key]}
									onCheckedChange={(checked) => setSelection((prev) => ({ ...prev, [key]: checked === true }))}
								/>
								<span className="grid min-w-0 text-sm">
									<span className="font-medium">
										{label}
										{!!docker?.[category].reclaimable && (
											<span className="font-normal text-muted-foreground">
												{" "}
												· up to {bytes(docker[category].reclaimable)}
											</span>
										)}
									</span>
									<span className="text-muted-foreground">{hint}</span>
								</span>
							</label>
						))}
					</div>
					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
						<AlertDialogAction disabled={!Object.values(selection).some(Boolean)} onClick={prune}>
							Free up space
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</Card>
	)
}

function CategoryStat({ label, color, category }: { label: string; color: string; category: DiskCategory }) {
	return (
		<div className="min-w-0">
			<div className="flex items-center gap-1.5 text-sm text-muted-foreground">
				<span className="size-2.5 rounded-sm shrink-0" style={{ backgroundColor: color }} />
				{label}
			</div>
			<div className="text-lg font-semibold leading-tight">{bytes(category.total)}</div>
			<div className="text-xs text-muted-foreground">
				{category.count} total{category.active ? `, ${category.active} in use` : ""}
				{!!category.reclaimable && <> · {bytes(category.reclaimable)} reclaimable</>}
			</div>
		</div>
	)
}

/** Rows with a bar sized relative to the largest item. */
function ItemList({ items, showKind, className }: { items: DiskItem[]; showKind?: boolean; className?: string }) {
	const max = Math.max(1, ...items.map((item) => item.size))
	return (
		<ul className={cn("grid gap-1.5", className)}>
			{items.map((item) => (
				<li
					key={`${item.kind}:${item.name}`}
					className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 text-sm"
				>
					<div className="relative min-w-0 h-7 rounded bg-muted/40 overflow-hidden">
						<div
							className="absolute inset-y-0 start-0 bg-primary/15"
							style={{ width: `${Math.max(1, (item.size / max) * 100)}%` }}
						/>
						<div className="relative flex items-center gap-2 h-full px-2 min-w-0">
							{showKind && <span className="text-xs text-muted-foreground shrink-0 w-16">{kindLabel[item.kind]}</span>}
							<span className="truncate font-mono" title={item.name}>
								{item.name}
							</span>
							{(item.kind === "image" || item.kind === "container" || item.kind === "volume") && !item.inUse && (
								<Badge variant="outline" className="shrink-0">
									unused
								</Badge>
							)}
						</div>
					</div>
					<span className="tabular-nums text-end min-w-16">{bytes(item.size)}</span>
				</li>
			))}
		</ul>
	)
}
