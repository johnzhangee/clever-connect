import React, { useState, useEffect } from 'react';
import { useAuthStore } from '../store/authStore';
import { useJobsStore } from '../store/jobsStore';
import { showGlobalAlert, showGlobalConfirm } from '../store/dialogStore';
import { 
	FiPlay, FiPause, FiTrash2, FiFolder, FiPlus, FiSettings, 
	FiGlobe, FiCheck, FiX, FiLink, FiDownloadCloud, FiServer,
	FiChevronLeft, FiFolderPlus, FiInfo, FiDownload, FiCloud
} from 'react-icons/fi';

interface LeechJob {
	id: string;
	url: string;
	filename: string;
	save_directory: string;
	total_bytes: number;
	downloaded: number;
	status: string;
	progress: number;
	speed: number;
	threads: number;
	error_message: string;
	file_exists?: boolean;
	s3_stored?: boolean;
	created_at: string;
}

interface LeechConfig {
	default_save_path: string;
	max_concurrent: number;
	threads_per_job: number;
	user_agent: string;
	proxy_url: string;
	premium_user_id?: string;
	premium_api_key?: string;
	auto_upload_to_telegram?: boolean;
	auto_upload_chat_id?: number;
}

const GameProgressBar: React.FC<{ progress: number; status: string }> = ({ progress, status }) => {
	const isDownloading = status === 'downloading';
	const isCompleted = status === 'completed';
	const isError = status === 'error';

	const outerCircleColor = isCompleted ? '#166534' : isError ? '#991b1b' : '#ea580c';
	const trackBorderColor = isCompleted ? '#22c55e' : isError ? '#ef4444' : '#f97316';
	const fillGradient = isCompleted 
		? 'linear-gradient(180deg, #4ade80 0%, #22c55e 100%)' 
		: isError 
		? 'linear-gradient(180deg, #f87171 0%, #ef4444 100%)' 
		: 'linear-gradient(180deg, #fbbf24 0%, #ea580c 100%)';

	const lightningColor = isCompleted ? '#4ade80' : isError ? '#f87171' : '#38bdf8';

	return (
		<div style={{ display: 'flex', alignItems: 'center', position: 'relative', width: '100%', height: 44, margin: '6px 0' }}>
			{/* Left badge circle */}
			<div style={{
				width: 44,
				height: 44,
				borderRadius: '50%',
				background: `linear-gradient(135deg, ${trackBorderColor} 0%, ${outerCircleColor} 100%)`,
				border: '2px solid rgba(255,255,255,0.2)',
				boxShadow: '0 3px 6px rgba(0,0,0,0.3), inset 0 2px 4px rgba(255,255,255,0.4)',
				display: 'flex',
				alignItems: 'center',
				justifyContent: 'center',
				zIndex: 10,
				position: 'absolute',
				left: 0,
			}}>
				{/* Inner yellow glass circle */}
				<div style={{
					width: 34,
					height: 34,
					borderRadius: '50%',
					background: 'linear-gradient(135deg, #fef08a 0%, #eab308 100%)',
					border: '1px solid rgba(255,255,255,0.6)',
					boxShadow: 'inset 0 1px 3px rgba(255,255,255,0.8), inset 0 -2px 4px rgba(0,0,0,0.2)',
					display: 'flex',
					alignItems: 'center',
					justifyContent: 'center',
					position: 'relative',
					overflow: 'hidden'
				}}>
					{/* Top glossy reflection overlay */}
					<div style={{
						position: 'absolute',
						top: 1,
						left: 4,
						right: 4,
						height: 12,
						background: 'linear-gradient(180deg, rgba(255,255,255,0.7) 0%, rgba(255,255,255,0) 100%)',
						borderRadius: '34px 34px 0 0',
						pointerEvents: 'none'
					}} />

					{/* Lightning Bolt SVG */}
					<svg 
						viewBox="0 0 24 24" 
						className={isDownloading ? 'pulse-lightning' : ''} 
						style={{ 
							width: 20, 
							height: 20, 
							fill: lightningColor,
							filter: `drop-shadow(0 1px 2px rgba(0,0,0,0.3))`
						}}
					>
						<path d="M19 9h-6l3-7L5 13h6l-3 9z" />
					</svg>
				</div>
			</div>

			{/* Right track wrapper */}
			<div style={{
				flex: 1,
				height: 28,
				background: '#27170a', // Dark warm brown-black track
				border: `2px solid ${trackBorderColor}`,
				borderRadius: 14,
				boxShadow: '0 2px 4px rgba(0,0,0,0.4), inset 0 2px 5px rgba(0,0,0,0.8)',
				padding: '2px',
				paddingLeft: 34, // Push starting point past circle boundary
				display: 'flex',
				alignItems: 'center',
				marginLeft: 10,
				position: 'relative',
				overflow: 'hidden'
			}}>
				{/* Inner progress fill bar */}
				<div style={{
					width: `${Math.max(3, progress)}%`, // min 3% to look nice inside track
					height: '100%',
					borderRadius: 10,
					background: fillGradient,
					position: 'relative',
					overflow: 'hidden',
					transition: 'width 0.4s cubic-bezier(0.4, 0, 0.2, 1)',
					boxShadow: '0 1px 2px rgba(0,0,0,0.2), inset 0 1px 0 rgba(255,255,255,0.4)'
				}}>
					{/* Animated Stripes overlay */}
					<div 
						className={isDownloading ? 'scroll-stripes' : ''}
						style={{
							position: 'absolute',
							top: 0,
							left: 0,
							right: 0,
							bottom: 0,
							backgroundImage: 'repeating-linear-gradient(45deg, rgba(255,255,255,0.18), rgba(255,255,255,0.18) 12px, transparent 12px, transparent 24px)',
							backgroundSize: '40px 100%'
						}}
					/>

					{/* Top gloss highlight bar overlay */}
					<div style={{
						position: 'absolute',
						top: 1,
						left: 4,
						right: 4,
						height: '40%',
						background: 'linear-gradient(180deg, rgba(255,255,255,0.5) 0%, rgba(255,255,255,0) 100%)',
						borderRadius: 6,
						pointerEvents: 'none'
					}} />
				</div>
			</div>
		</div>
	);
};

export const LeechPage: React.FC = () => {
	const { token } = useAuthStore();
	
	const jobs = useJobsStore(state => state.leechJobs);
	const initWebSocket = useJobsStore(state => state.initWebSocket);
	const sendAction = useJobsStore(state => state.sendAction);

	const [hoveredJobId, setHoveredJobId] = useState<string | null>(null);
	const [dismissedOverlays, setDismissedOverlays] = useState<Record<string, boolean>>({});

	const [config, setConfig] = useState<LeechConfig>({
		default_save_path: './downloads',
		max_concurrent: 3,
		threads_per_job: 8,
		user_agent: 'CleverConnect/1.0',
		proxy_url: '',
		auto_upload_to_telegram: false,
		auto_upload_chat_id: 0
	});

	// Modals State
	const [showAddModal, setShowAddModal] = useState(false);
	const [showConfigModal, setShowConfigModal] = useState(false);
	const [showFolderModal, setShowFolderModal] = useState(false);

	// Add Form State
	const [downloadUrl, setDownloadUrl] = useState('');
	const [saveDir, setSaveDir] = useState('');
	const [filename, setFilename] = useState('');
	const [threads, setThreads] = useState(8);
	const [downloadUsername, setDownloadUsername] = useState('');
	const [downloadPassword, setDownloadPassword] = useState('');
	const [usePremium, setUsePremium] = useState(false);
	const [isAdding, setIsAdding] = useState(false);
	const [addingProgress, setAddingProgress] = useState('');

	// Directory Picker State
	const [currentPath, setCurrentPath] = useState('/');
	const [directories, setDirectories] = useState<string[]>([]);
	const [newFolderName, setNewFolderName] = useState('');
	const [showNewFolderInput, setShowNewFolderInput] = useState(false);

	// Fetch Config
	const fetchConfig = async () => {
		try {
			const res = await fetch('/api/leech/config', {
				headers: { 'Authorization': `Bearer ${token}` }
			});
			if (res.ok) {
				const data = await res.json();
				setConfig(data);
				if (!saveDir) setSaveDir(data.default_save_path);
			}
		} catch (err) {
			console.error('Failed to fetch downloader configuration', err);
		}
	};

	useEffect(() => {
		if (token) {
			const close = initWebSocket(token);
			fetchConfig();
			return () => close();
		}
	}, [token, initWebSocket]);

	// Fetch remote directories for picker
	const fetchDirectories = async (path: string) => {
		try {
			const res = await fetch(`/api/files/list?path=${encodeURIComponent(path)}`, {
				headers: { 'Authorization': `Bearer ${token}` }
			});
			if (res.ok) {
				const data = await res.json();
				const dirs = (data.files || [])
					.filter((f: any) => f.is_dir)
					.map((f: any) => f.name);
				setDirectories(dirs);
				setCurrentPath(data.current_path || '/');
			}
		} catch (err) {
			console.error('Failed to fetch directories', err);
		}
	};

	// Open folder picker modal
	const openFolderPicker = () => {
		fetchDirectories(saveDir || currentPath);
		setShowFolderModal(true);
	};

	// Navigate up directory picker tree
	const handleDirUp = () => {
		if (currentPath === '/' || currentPath === '') return;
		const parts = currentPath.split('/');
		parts.pop();
		const parent = parts.join('/') || '/';
		fetchDirectories(parent);
	};

	// Navigate into subfolder in picker
	const handleDirSelect = (name: string) => {
		const next = currentPath === '/' ? `/${name}` : `${currentPath}/${name}`;
		fetchDirectories(next);
	};

	// Create new directory in picker modal
	const handleCreateDirectory = async () => {
		if (!newFolderName.trim()) return;
		try {
			const res = await fetch('/api/files/create-folder', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					'Authorization': `Bearer ${token}`
				},
				body: JSON.stringify({
					parent_path: currentPath,
					folder_name: newFolderName
				})
			});
			if (res.ok) {
				setNewFolderName('');
				setShowNewFolderInput(false);
				fetchDirectories(currentPath);
			} else {
				const err = await res.json();
				showGlobalAlert(`Failed: ${err.error || 'Unknown error'}`, { title: 'Directory Creation', variant: 'error' });
			}
		} catch (err) {
			console.error(err);
		}
	};

	// Start a download job
	const handleAddJob = async () => {
		const urls = downloadUrl.split(/[\r\n]+/).map(u => u.trim()).filter(Boolean);
		if (urls.length === 0) return;

		setIsAdding(true);
		try {
			for (let i = 0; i < urls.length; i++) {
				const url = urls[i];
				setAddingProgress(`Adding ${i + 1}/${urls.length}...`);
				const body: any = {
					url: url,
					save_directory: saveDir,
					threads: threads,
					username: downloadUsername,
					password: downloadPassword,
					use_premium: usePremium
				};
				if (urls.length === 1 && filename.trim()) {
					body.filename = filename;
				}

				const res = await fetch('/api/leech/add', {
					method: 'POST',
					headers: {
						'Content-Type': 'application/json',
						'Authorization': `Bearer ${token}`
					},
					body: JSON.stringify(body)
				});
				if (!res.ok) {
					const data = await res.json();
					console.error(`Failed to add URL ${url}:`, data.error || 'Unknown error');
				}
			}
			setShowAddModal(false);
			setDownloadUrl('');
			setFilename('');
			setDownloadUsername('');
			setDownloadPassword('');
			setUsePremium(false);
		} catch (err) {
			console.error(err);
		} finally {
			setIsAdding(false);
			setAddingProgress('');
		}
	};

	// Toggle pause/resume
	const handleToggleJob = (job: LeechJob) => {
		const wsAction = job.status === 'downloading' ? 'pause_leech' : 'resume_leech';
		sendAction({ action: wsAction, job_id: job.id });
	};

	// Delete a job
	const handleDeleteJob = async (id: string, deleteFiles: boolean) => {
		if (!(await showGlobalConfirm('Are you sure you want to delete this download job?', { title: 'Delete Job', variant: 'warning' }))) return;
		sendAction({ action: 'delete_leech', job_id: id, delete_files: deleteFiles });
	};

	// Update Config settings
	const handleSaveConfig = async () => {
		try {
			const res = await fetch('/api/leech/config', {
				method: 'POST',
				headers: {
					'Content-Type': 'application/json',
					'Authorization': `Bearer ${token}`
				},
				body: JSON.stringify(config)
			});
			if (res.ok) {
				setShowConfigModal(false);
				fetchConfig();
			}
		} catch (err) {
			console.error(err);
		}
	};

	// Bytes formatter helper
	const formatBytes = (bytes: number) => {
		if (bytes === 0) return '0 B';
		const k = 1024;
		const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
		const i = Math.floor(Math.log(bytes) / Math.log(k));
		return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
	};

	return (
		<div>
			<style>{`
				@keyframes lightningPulse {
					0%, 100% { transform: scale(1); filter: drop-shadow(0 0 2px rgba(56,189,248,0.5)); }
					50% { transform: scale(1.15); filter: drop-shadow(0 0 8px rgba(56,189,248,0.9)); }
				}
				@keyframes stripesScroll {
					from { background-position: 0 0; }
					to { background-position: 40px 0; }
				}
				.pulse-lightning {
					animation: lightningPulse 1.5s infinite ease-in-out;
					transform-origin: center;
				}
				.scroll-stripes {
					animation: stripesScroll 1s infinite linear;
				}
			`}</style>
			{/* Page Header */}
			<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 24 }}>
				<div>
					<h1 style={{ fontSize: 22, fontWeight: 700, color: 'var(--color-brand-heading)', margin: 0 }}>Leech Manager</h1>
					<p style={{ fontSize: 12, color: 'var(--color-brand-text)', margin: '4px 0 0' }}>Remote download, cache, and seed files directly to your server sandbox.</p>
				</div>
				<div style={{ display: 'flex', gap: 12 }}>
					<button className="btn btn--primary" onClick={() => setShowAddModal(true)} style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
						<FiPlus /> New Download
					</button>
					<button className="btn" onClick={() => setShowConfigModal(true)} style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
						<FiSettings /> Settings
					</button>
				</div>
			</div>

			{/* Jobs Queue Cards */}
			<div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
				{jobs.length === 0 ? (
					<div className="g-card" style={{ padding: '60px 20px', textAlign: 'center', color: 'var(--color-brand-muted)' }}>
						<FiDownloadCloud size={48} style={{ margin: '0 auto 16px', opacity: 0.3 }} />
						<div style={{ fontSize: 16, fontWeight: 600, color: 'var(--color-brand-heading)' }}>No Active Downloads</div>
						<p style={{ fontSize: 12, maxWidth: 300, margin: '6px auto 0', lineHeight: 1.4 }}>Paste a URL link to fetch archives, ISO images, or large assets directly on your server.</p>
					</div>
				) : (
					jobs.map(job => {
						const fileMissing = job.status === 'completed' && job.file_exists === false;
						const isHovered = hoveredJobId === job.id;
						const showOverlay = fileMissing && isHovered && !dismissedOverlays[job.id];

						return (
							<div 
								className="g-card" 
								key={job.id}
								onMouseEnter={() => setHoveredJobId(job.id)}
								onMouseLeave={() => {
									setHoveredJobId(null);
									if (dismissedOverlays[job.id]) {
										setDismissedOverlays(prev => ({ ...prev, [job.id]: false }));
									}
								}}
								style={{ 
									display: 'flex', 
									flexDirection: 'column', 
									gap: 12,
									position: 'relative',
									...(fileMissing ? {
										filter: 'grayscale(1) contrast(1.1) brightness(0.8)',
										border: '1px dashed #4b5563',
										background: '#121212',
									} : {})
								}}
							>
								{showOverlay && (
									<div className="missing-file-overlay" style={{
										position: 'absolute',
										top: 0,
										left: 0,
										right: 0,
										bottom: 0,
										background: 'rgba(0, 0, 0, 0.85)',
										zIndex: 50,
										display: 'flex',
										alignItems: 'center',
										justifyContent: 'center',
										gap: 12,
										borderRadius: 8,
									}}>
										<div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 10 }}>
											<div style={{ color: '#ef4444', fontSize: 13, fontWeight: 'bold' }}>
												File not found on disk or in S3
											</div>
											<div style={{ display: 'flex', gap: 12 }}>
												<button 
													className="btn btn--primary" 
													onClick={(e) => {
														e.stopPropagation();
														handleDeleteJob(job.id, false);
													}}
													style={{ 
														background: '#dc2626', 
														borderColor: '#dc2626',
														color: '#fff',
														padding: '6px 12px',
														fontSize: 12,
														fontWeight: 'bold',
														display: 'flex',
														alignItems: 'center',
														gap: 6
													}}
												>
													<FiTrash2 size={14} /> Delete Database Record
												</button>
												<button 
													className="btn" 
													onClick={(e) => {
														e.stopPropagation();
														setDismissedOverlays(prev => ({ ...prev, [job.id]: true }));
													}}
													style={{ 
														background: '#374151',
														borderColor: '#4b5563',
														color: '#fff',
														padding: '6px 12px',
														fontSize: 12
													}}
												>
													Cancel / View Info
												</button>
											</div>
										</div>
									</div>
								)}
								{/* Row header info */}
							<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: 16 }}>
								<div style={{ flex: 1, minWidth: 0 }}>
									<div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
										<span style={{ fontSize: 14, fontWeight: 700, color: 'var(--color-brand-heading)', wordBreak: 'break-all' }}>
											{job.filename}
										</span>
										<span style={{
											fontSize: 10,
											fontWeight: 700,
											padding: '2px 8px',
											borderRadius: 40,
											textTransform: 'uppercase',
											background: job.status === 'completed' ? 'rgba(34,197,94,0.1)' : job.status === 'downloading' ? 'rgba(59,130,246,0.1)' : 'rgba(107,114,128,0.1)',
											color: job.status === 'completed' ? '#22c55e' : job.status === 'downloading' ? '#3b82f6' : '#6b7280'
									}}>
										{job.status}
									</span>
									{job.s3_stored && (
										<span style={{
											fontSize: 10, fontWeight: 700, padding: '2px 8px', borderRadius: 40,
											textTransform: 'uppercase', background: 'rgba(59,130,246,0.1)',
											color: '#3b82f6', display: 'flex', alignItems: 'center', gap: 3
										}} title="File is archived in S3 Object Storage">
											<FiCloud size={9} /> S3
										</span>
									)}
								</div>
									<div style={{ display: 'flex', alignItems: 'center', gap: 12, marginTop: 4, flexWrap: 'wrap' }}>
										<span style={{ fontSize: 11, color: 'var(--color-brand-muted)', display: 'flex', alignItems: 'center', gap: 4 }}>
											<FiLink size={11} /> {job.url.length > 55 ? `${job.url.substring(0, 55)}...` : job.url}
										</span>
										<span style={{ fontSize: 11, color: 'var(--color-brand-text)', display: 'flex', alignItems: 'center', gap: 4 }}>
											<FiServer size={11} /> Save to: <code style={{ fontSize: 10, background: 'var(--color-brand-bg)', padding: '2px 4px', borderRadius: 4 }}>{job.save_directory}</code>
										</span>
									</div>
								</div>

								{/* Control Actions */}
								<div style={{ display: 'flex', gap: 8 }}>
									{job.status !== 'completed' && job.status !== 'error' && (
										<button 
											className="btn btn--sm" 
											onClick={() => handleToggleJob(job)}
											style={{ display: 'flex', alignItems: 'center', gap: 4 }}
										>
											{job.status === 'downloading' ? <FiPause size={12} /> : <FiPlay size={12} />}
											{job.status === 'downloading' ? 'Pause' : 'Resume'}
										</button>
									)}
									<button 
										className="btn btn--sm" 
										onClick={() => handleDeleteJob(job.id, false)}
										style={{ display: 'flex', alignItems: 'center', gap: 4, color: '#ef4444', borderColor: '#ef4444' }}
									>
										<FiTrash2 size={12} /> Clear
									</button>
									{job.status === 'completed' && (
										<>
											<a 
												href={`/api/files/download?path=${encodeURIComponent(`${job.save_directory}/${job.filename}`)}&token=${encodeURIComponent(token || '')}`}
												download
												className="btn btn--sm btn--primary"
												style={{ display: 'inline-flex', alignItems: 'center', gap: 4, textDecoration: 'none', height: 32 }}
											>
												<FiDownload size={12} /> Download
											</a>
											<button 
												className="btn btn--sm"
												onClick={() => handleDeleteJob(job.id, true)}
												style={{ color: '#ef4444', background: 'rgba(239,68,68,0.05)', borderColor: '#ef4444' }}
											>
												Delete File
											</button>
										</>
									)}
								</div>
							</div>

							{/* Progress Bar indicator */}
							<div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
								<GameProgressBar progress={job.progress} status={job.status} />
								<div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 11, color: 'var(--color-brand-text)', fontFamily: 'monospace' }}>
									<span>{formatBytes(job.downloaded)} / {formatBytes(job.total_bytes)} ({job.progress.toFixed(1)}%)</span>
									{job.status === 'downloading' && (
										<span style={{ color: 'var(--color-brand)', fontWeight: 600 }}>{job.speed.toFixed(2)} MB/s • {job.threads} Connections</span>
									)}
								</div>
							</div>

							{/* FlashGet Chunk Visualizer grid */}
							{(() => {
								const totalBlocks = 100;
								const completedCount = Math.floor((job.progress / 100) * totalBlocks);
								const isDownloading = job.status === 'downloading';
								const activeThreads = isDownloading ? Math.max(1, job.threads) : 0;

								const blocks = [];
								for (let i = 0; i < totalBlocks; i++) {
									let type: 'pending' | 'completed' | 'downloading' = 'pending';
									if (i < completedCount) {
										type = 'completed';
									} else if (isDownloading && i >= completedCount && i < completedCount + activeThreads && i < totalBlocks) {
										type = 'downloading';
									}
									blocks.push(type);
								}

								return (
									<div style={{ display: 'flex', flexDirection: 'column', gap: 6, marginTop: 8 }}>
										<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
											<span style={{ fontSize: 10, fontWeight: 700, color: 'var(--color-brand-muted)', letterSpacing: '0.3px' }}>GRID SEGMENT VIEWER (FLASHGET STYLE)</span>
											<span style={{ fontSize: 9, color: 'var(--color-brand-muted)', fontFamily: 'monospace' }}>
												{isDownloading ? `${activeThreads} segments pulling` : job.status === 'completed' ? '100% synced' : 'queued'}
											</span>
										</div>
										<div style={{
											display: 'grid',
											gridTemplateColumns: 'repeat(25, 1fr)',
											gap: 3,
											background: 'rgba(0,0,0,0.18)',
											border: '1px solid var(--color-brand-border)',
											padding: 6,
											borderRadius: 6
										}}>
											<style>{`
												@keyframes fgPulse {
													0% { opacity: 0.6; transform: scale(0.96); filter: brightness(1); }
													100% { opacity: 1; transform: scale(1.04); filter: brightness(1.3); }
												}
											`}</style>
											{blocks.map((blockType, idx) => {
												let bg = 'rgba(255, 255, 255, 0.05)';
												let border = '1px solid rgba(255, 255, 255, 0.03)';
												let animation = 'none';
												let boxS = 'none';

												if (blockType === 'completed') {
													bg = 'linear-gradient(135deg, var(--color-brand), #f59e0b)';
													border = '1px solid rgba(255, 255, 255, 0.08)';
												} else if (blockType === 'downloading') {
													bg = '#3b82f6';
													border = '1px solid #60a5fa';
													boxS = '0 0 8px #3b82f6';
													animation = 'fgPulse 0.8s infinite alternate';
												}

												return (
													<div 
														key={idx}
														style={{
															height: 10,
															borderRadius: 2,
															background: bg,
															border: border,
															boxShadow: boxS,
															animation: animation,
															transition: 'background 0.3s ease'
														}}
													/>
												);
											})}
										</div>
									</div>
								);
							})()}

							{/* Error log if failed */}
							{job.status === 'error' && job.error_message && (
								<div style={{
									background: 'rgba(239,68,68,0.05)',
									borderLeft: '3px solid #ef4444',
									padding: '8px 12px',
									borderRadius: 4,
									fontSize: 11,
									color: '#ef4444',
									display: 'flex',
									alignItems: 'center',
									gap: 6
								}}>
									<FiInfo /> Error: {job.error_message}
								</div>
							)}
						</div>
						);
					})
				)}
			</div>

			{/* Modal: New Download Job */}
			{showAddModal && (
				<div style={{
					position: 'fixed',
					top: 0,
					left: 0,
					width: '100vw',
					height: '100vh',
					background: 'rgba(0,0,0,0.5)',
					backdropFilter: 'blur(4px)',
					zIndex: 1000,
					display: 'flex',
					alignItems: 'center',
					justifyContent: 'center'
				}}>
					<div className="g-card" style={{ width: '100%', maxWidth: 500, display: 'flex', flexDirection: 'column', gap: 16 }}>
						<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
							<h2 style={{ fontSize: 18, fontWeight: 700, margin: 0, color: 'var(--color-brand-heading)' }}>Remote File Leech</h2>
							<button onClick={() => setShowAddModal(false)} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--color-brand-text)' }}><FiX size={18} /></button>
						</div>

						{/* Form inputs */}
						<div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
							<div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
								<label style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)' }}>Source Link URL(s) (One link per line)</label>
								<textarea 
									placeholder="https://example.com/archive1.zip&#10;https://example.com/archive2.zip" 
									value={downloadUrl} 
									onChange={(e) => setDownloadUrl(e.target.value)}
									disabled={isAdding}
									rows={4}
									style={{ width: '100%', padding: '10px 14px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', outline: 'none', color: 'var(--color-brand-heading)', resize: 'vertical', fontFamily: 'monospace', fontSize: 13 }}
								/>
							</div>

							<div style={{ display: 'flex', gap: 12 }}>
								<div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 4 }}>
									<label style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)' }}>Source User (Optional)</label>
									<input 
										type="text" 
										placeholder="username" 
										autoComplete="new-password"
										value={downloadUsername} 
										onChange={(e) => setDownloadUsername(e.target.value)}
										disabled={isAdding}
										style={{ width: '100%', padding: '10px 14px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', outline: 'none', color: 'var(--color-brand-heading)' }}
									/>
								</div>
								<div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 4 }}>
									<label style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)' }}>Source Pass (Optional)</label>
									<input 
										type="password" 
										placeholder="password" 
										autoComplete="new-password"
										value={downloadPassword} 
										onChange={(e) => setDownloadPassword(e.target.value)}
										disabled={isAdding}
										style={{ width: '100%', padding: '10px 14px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', outline: 'none', color: 'var(--color-brand-heading)' }}
									/>
								</div>
							</div>

							<div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
								<label style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)' }}>
									Custom Filename (Optional) {downloadUrl.split(/[\r\n]+/).map(u => u.trim()).filter(Boolean).length > 1 && <span style={{ color: '#f59e0b' }}>- Ignored for multi-links</span>}
								</label>
								<input 
									type="text" 
									placeholder="leave blank to auto-detect" 
									value={filename} 
									onChange={(e) => setFilename(e.target.value)}
									disabled={isAdding || downloadUrl.split(/[\r\n]+/).map(u => u.trim()).filter(Boolean).length > 1}
									style={{ width: '100%', padding: '10px 14px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', outline: 'none', color: 'var(--color-brand-heading)', opacity: downloadUrl.split(/[\r\n]+/).map(u => u.trim()).filter(Boolean).length > 1 ? 0.5 : 1 }}
								/>
							</div>

							<div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
								<label style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)' }}>Save Folder Path</label>
								<div style={{ display: 'flex', gap: 8 }}>
									<input 
										type="text" 
										readOnly
										value={saveDir} 
										style={{ flex: 1, padding: '10px 14px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-card)', color: 'var(--color-brand-heading)', outline: 'none' }}
									/>
									<button className="btn" onClick={openFolderPicker} disabled={isAdding} style={{ display: 'flex', alignItems: 'center', justifyContent: 'center' }}><FiFolder size={16} /></button>
								</div>
							</div>

							<div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
								<label style={{ display: 'flex', justifyContent: 'space-between', fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)' }}>
									<span>Parallel Request Threads</span>
									<span style={{ fontFamily: 'monospace' }}>{threads} connections</span>
								</label>
								<input 
									type="range" 
									min="1" 
									max="32" 
									value={threads} 
									onChange={(e) => setThreads(parseInt(e.target.value))}
									disabled={isAdding}
									style={{ width: '100%', accentColor: 'var(--color-brand)' }}
								/>
							</div>
							<div 
								style={{ 
									display: 'flex', 
									alignItems: 'center', 
									justifyContent: 'space-between', 
									padding: '12px 16px', 
									borderRadius: 8, 
									border: usePremium ? '1px solid rgba(234, 88, 12, 0.4)' : '1px solid var(--color-brand-border)', 
									background: usePremium ? 'rgba(234, 88, 12, 0.05)' : 'var(--color-brand-bg)',
									cursor: isAdding ? 'not-allowed' : 'pointer',
									transition: 'all 0.2s ease',
									marginTop: 4,
									opacity: isAdding ? 0.7 : 1
								}}
								onClick={() => !isAdding && setUsePremium(!usePremium)}
							>
								<div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
									<span style={{ fontSize: 13, fontWeight: 700, color: usePremium ? 'var(--color-brand)' : 'var(--color-brand-heading)' }}>
										Premium.to Link Leech
									</span>
									<span style={{ fontSize: 10, color: 'var(--color-brand-muted)' }}>
										Resolve host links (Rapidgator, etc.) via premium.to account
									</span>
								</div>
								
								<div style={{
									width: 40,
									height: 20,
									borderRadius: 20,
									background: usePremium ? 'var(--color-brand)' : '#334155',
									position: 'relative',
									transition: 'background 0.2s'
								}}>
									<div style={{
										width: 14,
										height: 14,
										borderRadius: '50%',
										background: '#fff',
										position: 'absolute',
										top: 3,
										left: usePremium ? 23 : 3,
										transition: 'left 0.2s cubic-bezier(0.4, 0, 0.2, 1)'
									}} />
								</div>
							</div>
						</div>

						{/* Action footer */}
						<div style={{ display: 'flex', justifyContent: 'end', gap: 12, marginTop: 12 }}>
							<button className="btn" onClick={() => setShowAddModal(false)} disabled={isAdding}>Cancel</button>
							<button className="btn btn--primary" onClick={handleAddJob} disabled={!downloadUrl.trim() || isAdding}>
								{isAdding ? addingProgress : 'Fetch Download'}
							</button>
						</div>
					</div>
				</div>
			)}

			{/* Modal: Remote Folder Picker Browser */}
			{showFolderModal && (
				<div style={{
					position: 'fixed',
					top: 0,
					left: 0,
					width: '100vw',
					height: '100vh',
					background: 'rgba(0,0,0,0.5)',
					backdropFilter: 'blur(4px)',
					zIndex: 1001,
					display: 'flex',
					alignItems: 'center',
					justifyContent: 'center'
				}}>
					<div className="g-card" style={{ width: '100%', maxWidth: 550, height: 480, display: 'flex', flexDirection: 'column' }}>
						<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 14 }}>
							<div>
								<h2 style={{ fontSize: 16, fontWeight: 700, margin: 0, color: 'var(--color-brand-heading)' }}>Select Destination</h2>
								<div style={{ fontSize: 11, color: 'var(--color-brand-muted)', fontFamily: 'monospace', marginTop: 2 }}>{currentPath}</div>
							</div>
							<button onClick={() => setShowFolderModal(false)} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--color-brand-text)' }}><FiX size={18} /></button>
						</div>

						{/* Browser controls */}
						<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 10, marginBottom: 12 }}>
							<button className="btn btn--sm" onClick={handleDirUp} disabled={currentPath === '/'} style={{ display: 'flex', alignItems: 'center', gap: 4 }}><FiChevronLeft /> Up</button>
							<button className="btn btn--sm" onClick={() => setShowNewFolderInput(true)} style={{ display: 'flex', alignItems: 'center', gap: 4 }}><FiFolderPlus /> New Folder</button>
						</div>

						{/* Create new folder inline input */}
						{showNewFolderInput && (
							<div style={{ display: 'flex', gap: 8, marginBottom: 12, background: 'var(--color-brand-bg)', padding: 8, borderRadius: 8, border: '1px solid var(--color-brand-border)' }}>
								<input 
									type="text" 
									placeholder="New folder name..." 
									value={newFolderName}
									onChange={(e) => setNewFolderName(e.target.value)}
									style={{ flex: 1, padding: '4px 8px', fontSize: 12, borderRadius: 4, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-card)', color: 'var(--color-brand-heading)', outline: 'none' }}
								/>
								<button className="btn btn--sm btn--primary" onClick={handleCreateDirectory} style={{ padding: '4px 10px' }}><FiCheck size={12} /></button>
								<button className="btn btn--sm" onClick={() => setShowNewFolderInput(false)} style={{ padding: '4px 10px' }}><FiX size={12} /></button>
							</div>
						)}

						{/* Directory grid explorer */}
						<div style={{ flex: 1, border: '1px solid var(--color-brand-border)', borderRadius: 8, background: 'var(--color-brand-bg)', overflowY: 'auto', padding: 8 }}>
							{directories.length === 0 ? (
								<div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: 'var(--color-brand-muted)', fontSize: 12 }}>
									No nested directories found
								</div>
							) : (
								<div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 8 }}>
									{directories.map(dir => (
										<div 
											key={dir} 
											onClick={() => handleDirSelect(dir)}
											style={{
												display: 'flex',
												alignItems: 'center',
												gap: 8,
												padding: '10px 12px',
												borderRadius: 6,
												background: 'var(--color-brand-card)',
												border: '1px solid var(--color-brand-border)',
												cursor: 'pointer',
												fontSize: 13,
												fontWeight: 500,
												color: 'var(--color-brand-heading)',
												transition: 'border-color 0.2s'
											}}
											className="file-card-hover"
										>
											<FiFolder style={{ color: 'var(--color-brand)', flexShrink: 0 }} />
											<span style={{ textOverflow: 'ellipsis', overflow: 'hidden', whiteSpace: 'nowrap' }}>{dir}</span>
										</div>
									))}
								</div>
							)}
						</div>

						{/* Confirm selection footer */}
						<div style={{ display: 'flex', justifyContent: 'end', gap: 12, marginTop: 14 }}>
							<button className="btn" onClick={() => setShowFolderModal(false)}>Cancel</button>
							<button className="btn btn--primary" onClick={() => {
								setSaveDir(currentPath);
								setShowFolderModal(false);
							}}>Select this folder</button>
						</div>
					</div>
				</div>
			)}

			{/* Modal: Downloader Advanced Settings */}
			{showConfigModal && (
				<div style={{
					position: 'fixed',
					top: 0,
					left: 0,
					width: '100vw',
					height: '100vh',
					background: 'rgba(0,0,0,0.5)',
					backdropFilter: 'blur(4px)',
					zIndex: 1000,
					display: 'flex',
					alignItems: 'center',
					justifyContent: 'center'
				}}>
					<div className="g-card" style={{ width: '100%', maxWidth: 500, display: 'flex', flexDirection: 'column', gap: 16 }}>
						<div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
							<h2 style={{ fontSize: 18, fontWeight: 700, margin: 0, color: 'var(--color-brand-heading)' }}>Downloader Settings</h2>
							<button onClick={() => setShowConfigModal(false)} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--color-brand-text)' }}><FiX size={18} /></button>
						</div>

						{/* Configuration inputs */}
						<div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
							<div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
								<label style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)' }}>Default Save Directory</label>
								<input 
									type="text" 
									value={config.default_save_path} 
									onChange={(e) => setConfig({ ...config, default_save_path: e.target.value })}
									style={{ width: '100%', padding: '10px 14px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', outline: 'none', color: 'var(--color-brand-heading)' }}
								/>
							</div>

							<div style={{ display: 'flex', flexDirection: 'row', gap: 12 }}>
								<div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 4 }}>
									<label style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)' }}>Max Queue Downloads</label>
									<input 
										type="number" 
										value={config.max_concurrent} 
										onChange={(e) => setConfig({ ...config, max_concurrent: parseInt(e.target.value) || 1 })}
										style={{ width: '100%', padding: '10px 14px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', outline: 'none', color: 'var(--color-brand-heading)' }}
									/>
								</div>
								<div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 4 }}>
									<label style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)' }}>Default Threads/Job</label>
									<input 
										type="number" 
										value={config.threads_per_job} 
										onChange={(e) => setConfig({ ...config, threads_per_job: parseInt(e.target.value) || 1 })}
										style={{ width: '100%', padding: '10px 14px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', outline: 'none', color: 'var(--color-brand-heading)' }}
									/>
								</div>
							</div>

							<div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
								<label style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)' }}>Client User-Agent Header</label>
								<input 
									type="text" 
									value={config.user_agent} 
									onChange={(e) => setConfig({ ...config, user_agent: e.target.value })}
									style={{ width: '100%', padding: '10px 14px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', outline: 'none', color: 'var(--color-brand-heading)' }}
								/>
							</div>

							<div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
								<label style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)' }}>Downloader Proxy URL (HTTP/SOCKS5)</label>
								<input 
									type="text" 
									placeholder="e.g. socks5://127.0.0.1:1080" 
									value={config.proxy_url} 
									onChange={(e) => setConfig({ ...config, proxy_url: e.target.value })}
									style={{ width: '100%', padding: '10px 14px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', outline: 'none', color: 'var(--color-brand-heading)' }}
								/>
							</div>

							<div style={{ display: 'flex', gap: 12 }}>
								<div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 4 }}>
									<label style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)' }}>Premium.to User ID</label>
									<input 
										type="text" 
										placeholder="e.g. GE4DGMJV"
										value={config.premium_user_id || ''} 
										onChange={(e) => setConfig({ ...config, premium_user_id: e.target.value })}
										style={{ width: '100%', padding: '10px 14px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', outline: 'none', color: 'var(--color-brand-heading)' }}
									/>
								</div>
								<div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 4 }}>
									<label style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)' }}>Premium.to API Key</label>
									<input 
										type="password" 
										placeholder="e.g. 9D0CEWH..."
										value={config.premium_api_key || ''} 
										onChange={(e) => setConfig({ ...config, premium_api_key: e.target.value })}
										style={{ width: '100%', padding: '10px 14px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', outline: 'none', color: 'var(--color-brand-heading)' }}
									/>
								</div>
							</div>

							<div 
								style={{ 
									display: 'flex', 
									alignItems: 'center', 
									justifyContent: 'space-between', 
									padding: '12px 16px', 
									borderRadius: 8, 
									border: config.auto_upload_to_telegram ? '1px solid rgba(14, 165, 233, 0.4)' : '1px solid var(--color-brand-border)', 
									background: config.auto_upload_to_telegram ? 'rgba(14, 165, 233, 0.05)' : 'var(--color-brand-bg)',
									cursor: 'pointer',
									transition: 'all 0.2s ease',
									marginTop: 4
								}}
								onClick={() => setConfig({ ...config, auto_upload_to_telegram: !config.auto_upload_to_telegram })}
							>
								<div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
									<span style={{ fontSize: 13, fontWeight: 700, color: config.auto_upload_to_telegram ? 'var(--color-brand)' : 'var(--color-brand-heading)' }}>
										Auto-Upload to Telegram
									</span>
									<span style={{ fontSize: 10, color: 'var(--color-brand-muted)' }}>
										Automatically queue Telegram upload when a leech job finishes
									</span>
								</div>
								
								<div style={{
									width: 40,
									height: 20,
									borderRadius: 20,
									background: config.auto_upload_to_telegram ? 'var(--color-brand)' : '#334155',
									position: 'relative',
									transition: 'background 0.2s'
								}}>
									<div style={{
										width: 14,
										height: 14,
										borderRadius: '50%',
										background: '#fff',
										position: 'absolute',
										top: 3,
										left: config.auto_upload_to_telegram ? 23 : 3,
										transition: 'left 0.2s cubic-bezier(0.4, 0, 0.2, 1)'
									}} />
								</div>
							</div>

							{config.auto_upload_to_telegram && (
								<div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
									<label style={{ fontSize: 11, fontWeight: 700, color: 'var(--color-brand-muted)' }}>Target Telegram Chat ID (Optional)</label>
									<input 
										type="number" 
										placeholder="e.g. -100123456789 (leave 0 for default admin chat)" 
										value={config.auto_upload_chat_id || ''} 
										onChange={(e) => setConfig({ ...config, auto_upload_chat_id: parseInt(e.target.value) || 0 })}
										style={{ width: '100%', padding: '10px 14px', borderRadius: 8, border: '1px solid var(--color-brand-border)', background: 'var(--color-brand-bg)', outline: 'none', color: 'var(--color-brand-heading)' }}
									/>
								</div>
							)}
						</div>

						{/* Save configurations */}
						<div style={{ display: 'flex', justifyContent: 'end', gap: 12, marginTop: 12 }}>
							<button className="btn" onClick={() => setShowConfigModal(false)}>Cancel</button>
							<button className="btn btn--primary" onClick={handleSaveConfig}>Save Configuration</button>
						</div>
					</div>
				</div>
			)}
		</div>
	);
};
