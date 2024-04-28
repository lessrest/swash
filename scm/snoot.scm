(use-modules (srfi srfi-9))

(define (basic-terminal)
  (let ((seq 1))
	(lambda (note)
	  (display (list seq note))
	  (newline)
	  (set! seq (+ seq 1)))))

(define note (basic-terminal))

(define (swash)
  (note "Open SWASH example.")
  (let ((stash #f))
	(call-with-prompt 'foo
	  ;; The thunk, or the prompt body.
	  (lambda ()
		(note "Open prompt body.")
		;; This will abort to the prompt, and display a value
		;; provided by the handler.
		(note (abort-to-prompt 'foo 'bar))
		;; The captured continuation starts here.
		(note "Close prompt body."))
	  
	  ;; The handler, or the prompt escape procedure.
	  ;; It's like a syscall or interrupt handler.
	  ;; It can resume the task by calling the captured continuation.
	  ;; It can also file away the continuation for later.
	  (lambda (continuation value)
		(note "Open escape procedure.")
		(note `(continuation ,continuation))
		(note `(value ,value))
		(set! stash (lambda () (continuation value)))
		(continuation "[hello]")
		(continuation "[hello again]")
		(note "Close escape procedure.")))

	(note "Close CALL-WITH-PROMPT.")
	(note "Calling STASH.")
	(stash)
	(note "Done.")))


;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;
;; Let's define a more sophisticated terminal output function,
;; that instead of a sequence number uses a hierarchy of tasks.

(define-record-type <task>
  (make-task id continuation parent)
  task?
  (id task-id)
  (continuation task-continuation set-task-continuation!)
  (parent task-parent))

(define current-task (make-parameter 
					  (make-task 'root (lambda (x) x) #f)))

(define (breadcrumb)
  ;; get the reverse stack of task ids
  (let loop ((task (current-task)))
	(if (task-parent task)
		(append (loop (task-parent task)) 
				(list (task-id task)))
		(list (task-id task)))))

(define (display-breadcrumb)
  (display (breadcrumb)))

(define (info . items)
  (display-breadcrumb)
  (newline)
  (display items)
  (newline))

(define (spawn id thunk)
  (let ((parent (current-task)))
	(call-with-prompt 'spawn
	  (lambda ()
		(parameterize ((current-task (make-task id thunk parent)))
		  (abort-to-prompt 'spawn '(resume))
		  (thunk)))
	  (lambda (k v)
		
		(k v)))))


;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;;

(define next-id!
  (let ((i 0))
	(lambda ()
	  (set! i (+ i 1))
	  i)))

(define (fyi item)
  (write "[fyi] " )
  (display item)
  (newline))

;;(define (task thunk)
;;  (let* ((self (next-id!))
;;		 (subtasks (list)))
;;	(define (handle-yield k)
;;	  (fyi `(task ,self (yield k)))
;;	  (call-with-prompt
;;		  'yield
;;		(lambda () 
;;		  ;; find next thing to do?
;;		  ;; should be a queue?
;;		  ;; a queue of continuations?
;;		  ;; 
;;		  ) 
;;		(lambda (thunk-continuation request)
;;		  (case (car request)
;;			((spawn)
;;			 (set! subtasks (cons (cadr request) subtasks))
;;			 ;; After spawning, yield.
;;			 (handle-yield thunk-continuation)
;;			 )
;;			((await)
;;			 (let ((promise (cadr request)))
;;			   (then promise (lambda (value)
;;							   (enqueue k value)))
;;			   )
;;			 )))))
;;	(yield )
;;	
;;	
;;	))
;;
